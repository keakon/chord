package desktop

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (s *System) Capabilities(ctx context.Context) (Capabilities, error) {
	if remote() || (os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "") {
		return Capabilities{Reason: ErrNoDesktop.Error()}, nil
	}
	_, gio := exec.LookPath("gio")
	_, xdg := exec.LookPath("xdg-open")
	return Capabilities{DefaultOpen: gio == nil || xdg == nil, Enumerate: gio == nil, Reason: ErrUnsupported.Error()}, ctx.Err()
}
func (s *System) queryAssociations(ctx context.Context, path string) (Associations, error) {
	c, err := s.Capabilities(ctx)
	if err != nil {
		return Associations{}, err
	}
	if !c.DefaultOpen {
		return Associations{}, ErrNoDesktop
	}
	if !c.Enumerate {
		return Associations{}, ErrUnsupported
	}
	if err := validateDocument(path); err != nil {
		return Associations{}, err
	}
	typ := documentMIME(path)
	data, err := s.run(ctx, true, "gio", "mime", typ)
	if err != nil {
		return Associations{}, err
	}
	var ids []string
	defaultID := ""
	seen := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "Default application for ") {
			if i := strings.LastIndex(line, ": "); i >= 0 {
				defaultID = strings.TrimSpace(line[i+2:])
				line = defaultID
			}
		}
		id := strings.TrimSpace(line)
		if !strings.HasSuffix(id, ".desktop") || strings.ContainsAny(id, "/\\ \t") {
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	entries, err := desktopEntries(ctx, ids)
	if err != nil {
		return Associations{}, err
	}
	result := Associations{}
	for _, id := range ids {
		if e, ok := entries[id]; ok {
			result.Apps = append(result.Apps, Application{ID: id, Name: e.name, Default: id == defaultID})
		}
	}
	if len(result.Apps) > 128 {
		result.Truncated = true
		result.Apps = result.Apps[:128]
	}
	return result, nil
}

type desktopEntry struct{ path, name string }

func desktopEntries(ctx context.Context, ids []string) (map[string]desktopEntry, error) {
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	home := os.Getenv("XDG_DATA_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".local", "share")
	}
	dirs := os.Getenv("XDG_DATA_DIRS")
	if dirs == "" {
		dirs = "/usr/local/share:/usr/share"
	}
	roots := append([]string{home}, filepath.SplitList(dirs)...)
	out := map[string]desktopEntry{}
	count := 0
	var total int64
	for _, base := range roots {
		if !filepath.IsAbs(base) {
			continue
		}
		root := filepath.Join(base, "applications")
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			if d.IsDir() {
				if strings.Count(rel, string(filepath.Separator)) >= 8 {
					return filepath.SkipDir
				}
				return nil
			}
			count++
			if count > 4096 {
				return errors.New("desktop application scan exceeded 4096 entries")
			}
			if d.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(rel, ".desktop") {
				return nil
			}
			id := strings.ReplaceAll(rel, string(filepath.Separator), "-")
			if !wanted[id] {
				return nil
			}
			if _, exists := out[id]; exists {
				return nil
			}
			info, e := d.Info()
			if e != nil || info.Size() > 64<<10 {
				return nil
			}
			total += info.Size()
			if total > 4<<20 {
				return errors.New("desktop entry read budget exceeded")
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return nil
			}
			values := map[string]string{}
			inEntry := false
			scan := bufio.NewScanner(strings.NewReader(string(raw)))
			for scan.Scan() {
				line := strings.TrimSpace(scan.Text())
				if strings.HasPrefix(line, "[") {
					inEntry = line == "[Desktop Entry]"
					continue
				}
				if inEntry {
					key, value, ok := strings.Cut(line, "=")
					if ok {
						values[key] = value
					}
				}
			}
			if values["Type"] != "Application" || values["Hidden"] == "true" {
				delete(wanted, id)
				return nil
			}
			out[id] = desktopEntry{path: path, name: values["Name"]}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *System) OpenDefault(ctx context.Context, path string) error {
	return s.launch(ctx, path, func() error {
		name := "gio"
		args := []string{"open", path}
		if _, err := exec.LookPath(name); err != nil {
			name = "xdg-open"
			args = []string{path}
		}
		_, err := s.run(ctx, false, name, args...)
		return err
	})
}
func (s *System) OpenWith(ctx context.Context, path, id string) error {
	return s.launch(ctx, path, func() error {
		list, err := s.queryAssociations(ctx, path)
		if err != nil {
			return err
		}
		valid := false
		for _, app := range list.Apps {
			if app.ID == id {
				valid = true
				break
			}
		}
		if !valid {
			return errors.New("application is no longer associated")
		}
		entries, err := desktopEntries(ctx, []string{id})
		if err != nil {
			return err
		}
		entry, ok := entries[id]
		if !ok {
			return errors.New("application is unavailable")
		}
		_, err = s.run(ctx, false, "gio", "launch", entry.path, path)
		return err
	})
}
func (s *System) ChooseApplication(context.Context, string) error { return ErrUnsupported }

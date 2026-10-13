package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
)

const associationScript = `ObjC.import('AppKit');
function run(argv) {
 const ws=$.NSWorkspace.sharedWorkspace;
 const u=$.NSURL.fileURLWithPath(argv[0]);
 const d=ws.URLForApplicationToOpenURL(u);
 const defaultID=d ? ObjC.unwrap(d.absoluteString) : '';
 const apps=ws.URLsForApplicationsToOpenURL(u);
 let result=[];
 for(let i=0;i<apps.count && i<129;i++) {
  const a=apps.objectAtIndex(i);
  result.push({id:ObjC.unwrap(a.absoluteString),name:ObjC.unwrap($.NSFileManager.defaultManager.displayNameAtPath(a.path)),default:ObjC.unwrap(a.absoluteString)===defaultID});
 }
 return JSON.stringify(result);
}`

func (s *System) Capabilities(ctx context.Context) (Capabilities, error) {
	if remote() {
		return Capabilities{Reason: ErrNoDesktop.Error()}, nil
	}
	return Capabilities{DefaultOpen: true, Enumerate: true}, ctx.Err()
}
func (s *System) queryAssociations(ctx context.Context, path string) (Associations, error) {
	if remote() {
		return Associations{}, ErrNoDesktop
	}
	if err := validateDocument(path); err != nil {
		return Associations{}, err
	}
	data, err := s.run(ctx, true, "/usr/bin/osascript", "-l", "JavaScript", "-e", associationScript, path)
	if err != nil {
		return Associations{}, err
	}
	var apps []Application
	if err = json.Unmarshal(data, &apps); err != nil {
		return Associations{}, err
	}
	result := Associations{Apps: apps, Truncated: len(apps) > 128}
	if result.Truncated {
		result.Apps = apps[:128]
	}
	return result, nil
}
func (s *System) OpenDefault(ctx context.Context, path string) error {
	return s.launch(ctx, path, func() error { _, err := s.run(ctx, false, "/usr/bin/open", path); return err })
}
func (s *System) OpenWith(ctx context.Context, path, id string) error {
	return s.launch(ctx, path, func() error {
		list, err := s.queryAssociations(ctx, path)
		if err != nil {
			return err
		}
		for _, a := range list.Apps {
			if a.ID == id {
				u, e := url.Parse(id)
				if e != nil || u.Scheme != "file" || u.Host != "" || !filepath.IsAbs(u.Path) {
					return errors.New("invalid application identity")
				}
				_, err = s.run(ctx, false, "/usr/bin/open", "-a", u.Path, path)
				return err
			}
		}
		return errors.New("application is no longer associated with this file")
	})
}
func (s *System) ChooseApplication(context.Context, string) error { return ErrUnsupported }

package inputhistory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"github.com/keakon/golog/log"
)

type diskView struct {
	ids                 map[string]int
	records             []Record
	sizes               []int64
	info                os.FileInfo
	repair              bool
	invalid, duplicates int
}

func (s *Store) readDisk() (diskView, error) {
	var d diskView
	f, err := os.OpenFile(s.path, os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	defer f.Close()
	if err = privateFile(f); err != nil {
		return d, err
	}
	d.info, err = f.Stat()
	if err != nil {
		return d, err
	}
	start := max(int64(0), d.info.Size()-s.opts.MaxBytes-MaxRecordBytes-1)
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return d, err
	}
	rd := bufio.NewReaderSize(f, 64<<10)
	if start > 0 {
		d.repair = true
		for {
			_, err = rd.ReadSlice('\n')
			if err != bufio.ErrBufferFull {
				break
			}
		}
	}
	var line []byte
	oversized := false
	seen := map[string]int{}
	for {
		chunk, e := rd.ReadSlice('\n')
		if len(line)+len(chunk) > MaxRecordBytes {
			oversized = true
		}
		if !oversized {
			line = append(line, chunk...)
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if e != nil && e != io.EOF {
			return d, e
		}
		if len(chunk) == 0 && len(line) == 0 && e == io.EOF {
			break
		}
		if oversized {
			d.invalid++
			d.repair = true
		} else {
			var r Record
			dec := json.NewDecoder(bytes.NewReader(line))
			dec.DisallowUnknownFields()
			valid := utf8.Valid(line) && dec.Decode(&r) == nil && r.Validate() == nil
			var extra any
			if valid && dec.Decode(&extra) != io.EOF {
				valid = false
			}
			if valid {
				if idx, ok := seen[r.ID]; ok {
					d.duplicates++
					d.records[idx] = Record{}
					d.sizes[idx] = 0
					d.repair = true
				}
				seen[r.ID] = len(d.records)
				d.records = append(d.records, r)
				d.sizes = append(d.sizes, int64(len(line)))
			} else {
				d.invalid++
				d.repair = true
			}
		}
		if e == io.EOF {
			d.repair = true
			break
		}
		line = nil
		oversized = false
	}
	records := d.records[:0]
	sizes := d.sizes[:0]
	for i, r := range d.records {
		if r.ID != "" {
			records = append(records, r)
			sizes = append(sizes, d.sizes[i])
		}
	}
	d.records = records
	d.sizes = sizes
	d.trim(s.opts.MaxEntries, s.opts.MaxBytes, false)
	d.indexIDs()
	if d.invalid > 0 || d.duplicates > 0 {
		old := s.diagnosticInfo
		if old == nil || !os.SameFile(old, d.info) || old.Size() != d.info.Size() || old.ModTime() != d.info.ModTime() {
			log.Warnf("input history skipped records path=%s invalid=%d duplicate_ids=%d", s.path, d.invalid, d.duplicates)
			s.diagnosticInfo = d.info
		}
	} else {
		s.diagnosticInfo = nil
	}
	return d, nil
}
func (d *diskView) trim(entries int, budget int64, keepLast bool) {
	var size int64
	for _, n := range d.sizes {
		size += n
	}
	cut := 0
	for len(d.records)-cut > entries || size > budget {
		if cut >= len(d.records) || (keepLast && len(d.records)-cut == 1) {
			break
		}
		size -= d.sizes[cut]
		cut++
	}
	if cut > 0 {
		d.records = append([]Record(nil), d.records[cut:]...)
		d.sizes = append([]int64(nil), d.sizes[cut:]...)
		d.repair = true
		d.indexIDs()
	}
}

func (d *diskView) indexIDs() {
	d.ids = make(map[string]int, len(d.records))
	for i, r := range d.records {
		d.ids[r.ID] = i
	}
}

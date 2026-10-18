//go:build !windows

package main

import "os"

// listDirFull on other systems exists only for the unit tests.
func listDirFull(path string) ([]dirEnt, error) {
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []dirEnt
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		de := dirEnt{name: e.Name(), dir: fi.IsDir() || (isLink(fi) && isDirLink(path, e.Name())), size: fi.Size(), alloc: -1, mtime: fi.ModTime().Unix(), mtimeNs: fi.ModTime().UnixNano(), link: isLink(fi)}
		if de.dir {
			de.size, de.alloc = 0, 0
		}
		out = append(out, de)
	}
	return out, nil
}

func isDirLink(dir, name string) bool {
	fi, err := os.Stat(dir + "/" + name)
	return err == nil && fi.IsDir()
}

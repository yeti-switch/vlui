package webui_test

import "io/fs"

// fsGlob lists the plain files directly under dir.
func fsGlob(f fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(f, dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

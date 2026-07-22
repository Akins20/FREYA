package skills

import (
	"archive/zip"
	"os"
)

// writeEvilZip builds an archive whose entry path escapes the extraction
// directory, for the zip-slip regression test.
func writeEvilZip(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	entry, err := w.Create("../../escaped.txt")
	if err != nil {
		return err
	}
	_, err = entry.Write([]byte("this should never land outside the destination"))
	return err
}

package shp

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Layer describes a shapefile to write into a zip archive via WriteZip.
type Layer struct {
	// Base is the file name prefix inside the archive, e.g. "sites" produces
	// sites.shp, sites.shx, sites.dbf and optional sites.prj/sites.cpg.
	Base string
	// Type is the geometry type of every shape in the layer.
	Type ShapeType
	// Populate writes the layer's shapes and attributes to w. It may call
	// SetProjection and SetEncoding before returning.
	Populate func(w *Writer) error
}

// WriteZip writes one or more shapefile layers into a single zip archive
// written to w.
func WriteZip(w io.Writer, layers ...Layer) error {
	if len(layers) == 0 {
		return errors.New("no layers provided")
	}
	dir, err := os.MkdirTemp("", "go-shp-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	zw := zip.NewWriter(w)
	for i, l := range layers {
		if err := writeLayerToZip(zw, dir, i, l); err != nil {
			zw.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("close zip: %w", err)
	}
	return nil
}

func writeLayerToZip(zw *zip.Writer, dir string, i int, l Layer) error {
	if l.Base == "" {
		return fmt.Errorf("layer %d: empty base name", i)
	}
	sw, err := Create(filepath.Join(dir, l.Base+".shp"), l.Type)
	if err != nil {
		return fmt.Errorf("layer %q: create shapefile: %w", l.Base, err)
	}
	if err := l.Populate(sw); err != nil {
		sw.Close()
		return fmt.Errorf("layer %q: populate: %w", l.Base, err)
	}
	if err := sw.Close(); err != nil {
		return fmt.Errorf("layer %q: close: %w", l.Base, err)
	}
	for _, ext := range []string{".shp", ".shx", ".dbf", ".prj", ".cpg"} {
		p := filepath.Join(dir, l.Base+ext)
		if err := addFileToZip(zw, p, l.Base+ext); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("layer %q: add %s: %w", l.Base, ext, err)
		}
	}
	return nil
}

func addFileToZip(zw *zip.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dst, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, f)
	return err
}

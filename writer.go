package shp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WriteSeekCloser is the union of io.Writer, io.Seeker and io.Closer. It is
// the minimum interface needed to write one component of a shapefile.
type WriteSeekCloser interface {
	io.Writer
	io.Seeker
	io.Closer
}

// Writer is the type that is used to write a new shapefile.
type Writer struct {
	filename     string
	shp          WriteSeekCloser
	shx          WriteSeekCloser
	GeometryType ShapeType
	num          int32
	bbox         Box

	dbf             WriteSeekCloser
	dbfFields       []Field
	dbfFieldsSet    bool
	dbfHeaderLength int16
	dbfRecordLength int16

	prj string
	cpg string
}

// errWriter captures the first write error so a sequence of writes can be
// checked once at the end.
type errWriter struct {
	io.Writer
	e error
}

func (ew *errWriter) Write(p []byte) (int, error) {
	if ew.e != nil {
		return 0, ew.e
	}
	n, err := ew.Writer.Write(p)
	if err != nil {
		ew.e = err
	}
	return n, err
}

// Create returns a new Writer backed by files named after filename. It is
// important to call Close when done because that method writes the headers for
// each file (SHP, SHX and DBF) and any sidecar files (PRJ, CPG).
// If filename ends in ".shp", it is treated as the basename and the ".shp"
// extension is not duplicated.
func Create(filename string, t ShapeType) (*Writer, error) {
	if strings.HasSuffix(strings.ToLower(filename), ".shp") {
		filename = filename[0 : len(filename)-4]
	}
	shp, err := os.Create(filename + ".shp")
	if err != nil {
		return nil, err
	}
	shx, err := os.Create(filename + ".shx")
	if err != nil {
		shp.Close()
		return nil, err
	}
	w, err := CreateFromWriteSeekClosers(shp, shx, nil, t)
	if err != nil {
		shp.Close()
		shx.Close()
		return nil, err
	}
	w.filename = filename
	return w, nil
}

// CreateFromWriteSeekClosers returns a new Writer writing the SHP and SHX
// components to the provided write-seek-closers. dbf may be nil; in that case
// a DBF is only produced if SetFields is called with a filename-backed writer.
// It is important to call Close when done so headers are written.
func CreateFromWriteSeekClosers(shp, shx, dbf WriteSeekCloser, t ShapeType) (*Writer, error) {
	if _, err := shp.Seek(100, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek shp header: %w", err)
	}
	if _, err := shx.Seek(100, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek shx header: %w", err)
	}
	return &Writer{
		shp:          shp,
		shx:          shx,
		dbf:          dbf,
		GeometryType: t,
	}, nil
}

// Append returns a Writer that will append to the given shapefile and the
// first error that was encountered during creation of that Writer. The
// shapefile must have a valid index file.
func Append(filename string) (*Writer, error) {
	shp, err := os.OpenFile(filename, os.O_RDWR, 0666)
	if err != nil {
		return nil, err
	}
	ext := filepath.Ext(filename)
	basename := filename[:len(filename)-len(ext)]
	w := &Writer{
		filename: basename,
		shp:      shp,
	}
	_, err = shp.Seek(32, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("cannot seek to SHP geometry type: %v", err)
	}
	err = binary.Read(shp, binary.LittleEndian, &w.GeometryType)
	if err != nil {
		return nil, fmt.Errorf("cannot read geometry type: %v", err)
	}
	er := &errReader{Reader: shp}
	w.bbox.MinX = readFloat64(er)
	w.bbox.MinY = readFloat64(er)
	w.bbox.MaxX = readFloat64(er)
	w.bbox.MaxY = readFloat64(er)
	if er.e != nil {
		return nil, fmt.Errorf("cannot read bounding box: %v", er.e)
	}

	shx, err := os.OpenFile(basename+".shx", os.O_RDWR, 0666)
	if err != nil {
		return nil, fmt.Errorf("cannot open shapefile index: %v", err)
	}
	_, err = shx.Seek(-8, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("cannot seek to last shape index: %v", err)
	}
	var offset int32
	err = binary.Read(shx, binary.BigEndian, &offset)
	if err != nil {
		return nil, fmt.Errorf("cannot read last shape index: %v", err)
	}
	offset = offset * 2
	_, err = shp.Seek(int64(offset), io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("cannot seek to last shape: %v", err)
	}
	err = binary.Read(shp, binary.BigEndian, &w.num)
	if err != nil {
		return nil, fmt.Errorf("cannot read number of last shape: %v", err)
	}
	_, err = shp.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("cannot seek to SHP end: %v", err)
	}
	_, err = shx.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("cannot seek to SHX end: %v", err)
	}
	w.shx = shx

	dbf, err := os.OpenFile(basename+".dbf", os.O_RDWR, 0666)
	if os.IsNotExist(err) {
		return w, nil // it's okay if the DBF does not exist
	}
	if err != nil {
		return nil, fmt.Errorf("cannot open DBF: %v", err)
	}

	_, err = dbf.Seek(8, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("cannot seek in DBF: %v", err)
	}
	err = binary.Read(dbf, binary.LittleEndian, &w.dbfHeaderLength)
	if err != nil {
		return nil, fmt.Errorf("cannot read header length from DBF: %v", err)
	}
	err = binary.Read(dbf, binary.LittleEndian, &w.dbfRecordLength)
	if err != nil {
		return nil, fmt.Errorf("cannot read record length from DBF: %v", err)
	}

	_, err = dbf.Seek(20, io.SeekCurrent) // skip padding
	if err != nil {
		return nil, fmt.Errorf("cannot seek in DBF: %v", err)
	}
	numFields := int(math.Floor(float64(w.dbfHeaderLength-33) / 32.0))
	w.dbfFields = make([]Field, numFields)
	err = binary.Read(dbf, binary.LittleEndian, &w.dbfFields)
	if err != nil {
		return nil, fmt.Errorf("cannot read number of fields from DBF: %v", err)
	}
	_, err = dbf.Seek(0, io.SeekEnd) // skip padding
	if err != nil {
		return nil, fmt.Errorf("cannot seek to DBF end: %v", err)
	}
	w.dbf = dbf
	w.dbfFieldsSet = true

	return w, nil
}

// Write writes a shape to the shapefile. This also creates a record in the
// SHX file and DBF file (if it is initialized). It returns the index of the
// written object, which can be used with WriteAttribute.
func (w *Writer) Write(shape Shape) (int32, error) {
	if w.num == 0 {
		w.bbox = shape.BBox()
	} else {
		w.bbox.Extend(shape.BBox())
	}
	w.num++

	if err := binary.Write(w.shp, binary.BigEndian, w.num); err != nil {
		return -1, fmt.Errorf("write record number: %w", err)
	}
	if _, err := w.shp.Seek(4, io.SeekCurrent); err != nil {
		return -1, fmt.Errorf("seek content length field: %w", err)
	}
	start, err := w.shp.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1, fmt.Errorf("seek record start: %w", err)
	}
	if err := binary.Write(w.shp, binary.LittleEndian, w.GeometryType); err != nil {
		return -1, fmt.Errorf("write shape type: %w", err)
	}

	ew := &errWriter{Writer: w.shp}
	shape.write(ew)
	if ew.e != nil {
		return -1, fmt.Errorf("write shape payload: %w", ew.e)
	}

	finish, err := w.shp.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1, fmt.Errorf("seek record end: %w", err)
	}
	length := int32(math.Floor((float64(finish) - float64(start)) / 2.0))
	if _, err := w.shp.Seek(start-4, io.SeekStart); err != nil {
		return -1, fmt.Errorf("seek content length field: %w", err)
	}
	if err := binary.Write(w.shp, binary.BigEndian, length); err != nil {
		return -1, fmt.Errorf("write content length: %w", err)
	}
	if _, err := w.shp.Seek(finish, io.SeekStart); err != nil {
		return -1, fmt.Errorf("seek record end: %w", err)
	}

	if err := binary.Write(w.shx, binary.BigEndian, int32((start-8)/2)); err != nil {
		return -1, fmt.Errorf("write shx offset: %w", err)
	}
	if err := binary.Write(w.shx, binary.BigEndian, length); err != nil {
		return -1, fmt.Errorf("write shx length: %w", err)
	}

	if w.dbf != nil {
		if err := w.writeEmptyRecord(); err != nil {
			return -1, fmt.Errorf("write DBF empty record: %w", err)
		}
	}

	return w.num - 1, nil
}

// SetProjection records the WKT projection string written to a .prj sidecar
// file when Close is called. It is only effective for filename-backed writers.
func (w *Writer) SetProjection(wkt string) {
	w.prj = wkt
}

// SetEncoding records the code page written to a .cpg sidecar file when Close
// is called (e.g. "UTF-8"). It is only effective for filename-backed writers.
func (w *Writer) SetEncoding(code string) {
	w.cpg = code
}

// Close writes the headers for the SHP, SHX and DBF files, writes any PRJ/CPG
// sidecar files, and closes all underlying files. It must be called when
// writing is done. The returned error aggregates every failure encountered.
func (w *Writer) Close() error {
	if (w.prj != "" || w.cpg != "") && w.filename == "" {
		return errors.New("projection/encoding set but writer has no filename for sidecar files")
	}

	var errs []error
	if err := w.writeHeader(w.shx); err != nil {
		errs = append(errs, fmt.Errorf("write shx header: %w", err))
	}
	if err := w.writeHeader(w.shp); err != nil {
		errs = append(errs, fmt.Errorf("write shp header: %w", err))
	}

	if w.dbf == nil && w.filename != "" {
		if err := w.SetFields([]Field{}); err != nil {
			errs = append(errs, fmt.Errorf("initialize empty DBF: %w", err))
		}
	}
	if w.dbf != nil {
		if err := w.writeDbfHeader(w.dbf); err != nil {
			errs = append(errs, fmt.Errorf("write DBF header: %w", err))
		}
	}

	if w.filename != "" {
		if w.prj != "" {
			if err := os.WriteFile(w.filename+".prj", []byte(w.prj), 0644); err != nil {
				errs = append(errs, fmt.Errorf("write .prj: %w", err))
			}
		}
		if w.cpg != "" {
			if err := os.WriteFile(w.filename+".cpg", []byte(w.cpg), 0644); err != nil {
				errs = append(errs, fmt.Errorf("write .cpg: %w", err))
			}
		}
	}

	if err := w.shp.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close shp: %w", err))
	}
	if err := w.shx.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close shx: %w", err))
	}
	if w.dbf != nil {
		if err := w.dbf.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close dbf: %w", err))
		}
	}

	return errors.Join(errs...)
}

// writeHeader writes the SHP/SHX header to ws.
func (w *Writer) writeHeader(ws io.WriteSeeker) error {
	filelength, err := ws.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if filelength == 0 {
		filelength = 100
	}
	if _, err := ws.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.BigEndian, []int32{9994, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.BigEndian, int32(filelength/2)); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.LittleEndian, []int32{1000, int32(w.GeometryType)}); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.LittleEndian, w.bbox); err != nil {
		return err
	}
	return binary.Write(ws, binary.LittleEndian, []float64{0.0, 0.0, 0.0, 0.0})
}

// writeDbfHeader writes a DBF header to ws.
func (w *Writer) writeDbfHeader(ws io.WriteSeeker) error {
	if _, err := ws.Seek(0, 0); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.LittleEndian, []byte{3, 24, 5, 3}); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.LittleEndian, w.num); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.LittleEndian, []int16{w.dbfHeaderLength, w.dbfRecordLength}); err != nil {
		return err
	}
	if err := binary.Write(ws, binary.LittleEndian, make([]byte, 20)); err != nil {
		return err
	}
	for _, field := range w.dbfFields {
		if err := binary.Write(ws, binary.LittleEndian, field); err != nil {
			return err
		}
	}
	_, err := ws.Write([]byte("\r"))
	return err
}

// SetFields sets field values in the DBF. It initializes the DBF file and
// should be used prior to writing any attributes.
func (w *Writer) SetFields(fields []Field) error {
	for i, field := range fields {
		name := field.String()
		if err := validateFieldName(name); err != nil {
			return fmt.Errorf("field %d (%q): %w", i, name, err)
		}
	}
	if w.dbfFieldsSet {
		return errors.New("cannot set fields: DBF fields already set")
	}
	if w.dbf == nil {
		if w.filename == "" {
			return errors.New("cannot initialize DBF: no filename and no DBF writer")
		}
		var err error
		w.dbf, err = os.Create(w.filename + ".dbf")
		if err != nil {
			return fmt.Errorf("failed to open %s.dbf: %w", w.filename, err)
		}
	}
	w.dbfFields = fields
	w.dbfFieldsSet = true

	w.dbfRecordLength = int16(1)
	for _, field := range fields {
		w.dbfRecordLength += int16(field.Size)
	}
	w.dbfHeaderLength = int16(len(fields)*32 + 33)

	buf := make([]byte, w.dbfHeaderLength)
	if _, err := w.dbf.Write(buf); err != nil {
		return fmt.Errorf("write DBF header space: %w", err)
	}
	for n := int32(0); n < w.num; n++ {
		if err := w.writeEmptyRecord(); err != nil {
			return err
		}
	}
	return nil
}

// writeEmptyRecord writes an empty record to the end of the DBF. This works by
// seeking to the end of the file and writing dbfRecordLength bytes. The first
// byte is a space that indicates a new record.
func (w *Writer) writeEmptyRecord() error {
	if _, err := w.dbf.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	buf := make([]byte, w.dbfRecordLength)
	buf[0] = ' '
	_, err := w.dbf.Write(buf)
	return err
}

// WriteAttribute writes value for field into the given row in the DBF. Row
// number should be the same as the order the Shape was written to the
// shapefile. The field value corresponds to the field in the slice used in
// SetFields.
func (w *Writer) WriteAttribute(row int, field int, value interface{}) error {
	if w.dbf == nil {
		return errors.New("initialize DBF by using SetFields first")
	}
	if field < 0 || field >= len(w.dbfFields) {
		return fmt.Errorf("field index %d out of range [0,%d)", field, len(w.dbfFields))
	}

	buf, err := formatAttribute(value, w.dbfFields[field].Precision)
	if err != nil {
		return err
	}

	if sz := int(w.dbfFields[field].Size); len(buf) > sz {
		return fmt.Errorf("unable to write field %d (%s): %q exceeds field length %d", field, w.dbfFields[field].String(), buf, sz)
	}

	seekTo := 1 + int64(w.dbfHeaderLength) + (int64(row) * int64(w.dbfRecordLength))
	for n := 0; n < field; n++ {
		seekTo += int64(w.dbfFields[n].Size)
	}
	if _, err := w.dbf.Seek(seekTo, io.SeekStart); err != nil {
		return err
	}
	_, err = w.dbf.Write(buf)
	return err
}

// formatAttribute converts a value to its DBF byte representation. Supported
// types are string, int, int32, int64, float64, bool, time.Time, and pointers
// to those types (a nil pointer produces an empty value). Floats are formatted
// with the given decimal precision.
func formatAttribute(value interface{}, precision uint8) ([]byte, error) {
	switch v := value.(type) {
	case string:
		return []byte(v), nil
	case int:
		return []byte(strconv.Itoa(v)), nil
	case int32:
		return []byte(strconv.FormatInt(int64(v), 10)), nil
	case int64:
		return []byte(strconv.FormatInt(v, 10)), nil
	case float64:
		return []byte(strconv.FormatFloat(v, 'f', int(precision), 64)), nil
	case bool:
		if v {
			return []byte("T"), nil
		}
		return []byte("F"), nil
	case time.Time:
		return []byte(v.Format("20060102")), nil
	case *string:
		if v == nil {
			return nil, nil
		}
		return []byte(*v), nil
	case *int32:
		if v == nil {
			return nil, nil
		}
		return []byte(strconv.FormatInt(int64(*v), 10)), nil
	case *int64:
		if v == nil {
			return nil, nil
		}
		return []byte(strconv.FormatInt(*v, 10)), nil
	case *float64:
		if v == nil {
			return nil, nil
		}
		return []byte(strconv.FormatFloat(*v, 'f', int(precision), 64)), nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", value)
	}
}

// BBox returns the bounding box of the Writer.
func (w *Writer) BBox() Box {
	return w.bbox
}

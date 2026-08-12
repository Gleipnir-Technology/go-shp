package shp

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var filenamePrefix = "test_files/write_"

func removeShapefile(filename string) {
	os.Remove(filename + ".shp")
	os.Remove(filename + ".shx")
	os.Remove(filename + ".dbf")
	os.Remove(filename + ".prj")
	os.Remove(filename + ".cpg")
}

func pointsToFloats(points []Point) [][]float64 {
	floats := make([][]float64, len(points))
	for k, v := range points {
		floats[k] = make([]float64, 2)
		floats[k][0] = v.X
		floats[k][1] = v.Y
	}
	return floats
}

func TestAppend(t *testing.T) {
	filename := filenamePrefix + "point"
	defer removeShapefile(filename)
	points := [][]float64{
		{0.0, 0.0},
		{5.0, 5.0},
		{10.0, 10.0},
	}

	shape, err := Create(filename+".shp", POINT)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range points {
		if _, err := shape.Write(&Point{p[0], p[1]}); err != nil {
			t.Fatal(err)
		}
	}
	wantNum := shape.num
	if err := shape.Close(); err != nil {
		t.Fatal(err)
	}

	newPoints := [][]float64{
		{15.0, 15.0},
		{20.0, 20.0},
		{25.0, 25.0},
	}
	shape, err = Append(filename + ".shp")
	if err != nil {
		t.Fatal(err)
	}
	if shape.GeometryType != POINT {
		t.Fatalf("wanted geo type %d, got %d", POINT, shape.GeometryType)
	}
	if shape.num != wantNum {
		t.Fatalf("wrong 'num', wanted type %d, got %d", wantNum, shape.num)
	}

	for _, p := range newPoints {
		if _, err := shape.Write(&Point{p[0], p[1]}); err != nil {
			t.Fatal(err)
		}
	}

	points = append(points, newPoints...)

	shapes := getShapesFromFile(filename, t)
	if len(shapes) != len(points) {
		t.Error("Number of shapes read was wrong")
	}
	testPoint(t, points, shapes)
}

func TestWritePoint(t *testing.T) {
	filename := filenamePrefix + "point"
	defer removeShapefile(filename)

	points := [][]float64{
		{0.0, 0.0},
		{5.0, 5.0},
		{10.0, 10.0},
	}

	shape, err := Create(filename+".shp", POINT)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range points {
		if _, err := shape.Write(&Point{p[0], p[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := shape.Close(); err != nil {
		t.Fatal(err)
	}

	shapes := getShapesFromFile(filename, t)
	if len(shapes) != len(points) {
		t.Error("Number of shapes read was wrong")
	}
	testPoint(t, points, shapes)
}

func TestWritePolyLine(t *testing.T) {
	filename := filenamePrefix + "polyline"
	defer removeShapefile(filename)

	points := [][]Point{
		{Point{0.0, 0.0}, Point{5.0, 5.0}},
		{Point{10.0, 10.0}, Point{15.0, 15.0}},
	}

	shape, err := Create(filename+".shp", POLYLINE)
	if err != nil {
		t.Fatal(err)
	}

	l := NewPolyLine(points)

	lWant := &PolyLine{
		Box:       Box{MinX: 0, MinY: 0, MaxX: 15, MaxY: 15},
		NumParts:  2,
		NumPoints: 4,
		Parts:     []int32{0, 2},
		Points: []Point{{X: 0, Y: 0},
			{X: 5, Y: 5},
			{X: 10, Y: 10},
			{X: 15, Y: 15},
		},
	}
	if !reflect.DeepEqual(l, lWant) {
		t.Errorf("incorrect NewLine: have: %+v; want: %+v", l, lWant)
	}

	if _, err := shape.Write(l); err != nil {
		t.Fatal(err)
	}
	if err := shape.Close(); err != nil {
		t.Fatal(err)
	}

	shapes := getShapesFromFile(filename, t)
	if len(shapes) != 1 {
		t.Error("Number of shapes read was wrong")
	}
	testPolyLine(t, pointsToFloats(flatten(points)), shapes)
}

type seekTracker struct {
	io.Writer
	offset int64
}

func (s *seekTracker) Seek(offset int64, whence int) (int64, error) {
	s.offset = offset
	return s.offset, nil
}

func (s *seekTracker) Close() error {
	return nil
}

func TestWriteAttribute(t *testing.T) {
	buf := new(bytes.Buffer)
	s := &seekTracker{Writer: buf}
	aString, err := StringField("A_STRING", 6)
	if err != nil {
		t.Fatal(err)
	}
	aFloat, err := FloatField("A_FLOAT", 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	anInt, err := NumberField("AN_INT", 4)
	if err != nil {
		t.Fatal(err)
	}
	w := Writer{
		dbf:             s,
		dbfFields:       []Field{aString, aFloat, anInt},
		dbfRecordLength: 100,
	}

	tests := []struct {
		name       string
		row        int
		field      int
		data       interface{}
		wantOffset int64
		wantData   string
	}{
		{"string-0", 0, 0, "test", 1, "test"},
		{"string-0-overflow-1", 0, 0, "overflo", 0, ""},
		{"string-0-overflow-n", 0, 0, "overflowing", 0, ""},
		{"string-3", 3, 0, "things", 301, "things"},
		{"float-0", 0, 1, 123.44, 7, "123.4400"},
		{"float-0-overflow-1", 0, 1, 1234.0, 0, ""},
		{"float-0-overflow-n", 0, 1, 123456789.0, 0, ""},
		{"int-0", 0, 2, 4242, 15, "4242"},
		{"int-0-overflow-1", 0, 2, 42424, 0, ""},
		{"int-0-overflow-n", 0, 2, 42424343, 0, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buf.Reset()
			s.offset = 0

			err := w.WriteAttribute(test.row, test.field, test.data)

			if buf.String() != test.wantData {
				t.Errorf("got data: %v, want: %v", buf.String(), test.wantData)
			}
			if s.offset != test.wantOffset {
				t.Errorf("got seek offset: %v, want: %v", s.offset, test.wantOffset)
			}
			if err == nil && test.wantData == "" {
				t.Error("got no data and no error")
			}
		})
	}
}

func TestFieldNameValidation(t *testing.T) {
	if _, err := StringField("", 10); err == nil {
		t.Error("expected error for empty field name")
	}
	if _, err := StringField("TOO_LONG_NAME", 10); err == nil {
		t.Error("expected error for over-long field name")
	}
	if _, err := StringField("OK", 10); err != nil {
		t.Errorf("unexpected error for valid name: %v", err)
	}
	if _, err := StringField("1234567890", 10); err != nil {
		t.Errorf("unexpected error for 10-char name: %v", err)
	}
}

func TestNewPolygonFromRings(t *testing.T) {
	ring := []Point{{0, 0}, {0, 1}, {1, 1}, {1, 0}}
	p := NewPolygonFromRings([][]Point{ring})
	if p.NumParts != 1 {
		t.Fatalf("NumParts = %d, want 1", p.NumParts)
	}
	if p.NumPoints != 5 {
		t.Fatalf("NumPoints = %d, want 5 (closed ring)", p.NumPoints)
	}
	if p.Points[0] != p.Points[len(p.Points)-1] {
		t.Error("ring not closed")
	}

	closedRing := append(append([]Point{}, ring...), ring[0])
	p2 := NewPolygonFromRings([][]Point{closedRing})
	if p2.NumPoints != 5 {
		t.Fatalf("NumPoints = %d, want 5 (already closed)", p2.NumPoints)
	}
}

func TestWriteCloseSidecars(t *testing.T) {
	filename := filenamePrefix + "sidecar"
	defer removeShapefile(filename)
	shape, err := Create(filename+".shp", POINT)
	if err != nil {
		t.Fatal(err)
	}
	const wkt = `GEOGCS["WGS 84",DATUM["WGS_1984",SPHEROID["WGS 84",6378137,298.257223563]]]`
	shape.SetProjection(wkt)
	shape.SetEncoding("UTF-8")
	if _, err := shape.Write(&Point{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := shape.Close(); err != nil {
		t.Fatal(err)
	}

	prj, err := os.ReadFile(filename + ".prj")
	if err != nil {
		t.Fatalf("read .prj: %v", err)
	}
	if string(prj) != wkt {
		t.Errorf(".prj content = %q, want %q", prj, wkt)
	}
	cpg, err := os.ReadFile(filename + ".cpg")
	if err != nil {
		t.Fatalf("read .cpg: %v", err)
	}
	if string(cpg) != "UTF-8" {
		t.Errorf(".cpg content = %q, want %q", cpg, "UTF-8")
	}
}

func ptrFloat(f float64) *float64 { return &f }

func TestFormatAttribute(t *testing.T) {
	tests := []struct {
		name      string
		value     interface{}
		precision uint8
		want      string
		wantErr   bool
	}{
		{"string", "hi", 0, "hi", false},
		{"int", 42, 0, "42", false},
		{"int32", int32(-42), 0, "-42", false},
		{"int64", int64(123456), 0, "123456", false},
		{"float64", 3.14159, 2, "3.14", false},
		{"bool-true", true, 0, "T", false},
		{"bool-false", false, 0, "F", false},
		{"time", time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), 0, "20260812", false},
		{"nil", nil, 0, "", false},
		{"nil-float-ptr", (*float64)(nil), 0, "", false},
		{"float-ptr", ptrFloat(2.5), 2, "2.50", false},
		{"unsupported", []int{1}, 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := formatAttribute(tt.value, tt.precision)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type failingWriteSeekCloser struct{}

func (f *failingWriteSeekCloser) Write(p []byte) (int, error) { return 0, io.ErrShortWrite }
func (f *failingWriteSeekCloser) Seek(offset int64, whence int) (int64, error) {
	return offset, nil
}
func (f *failingWriteSeekCloser) Close() error { return nil }

func TestWritePropagatesError(t *testing.T) {
	buf := new(bytes.Buffer)
	shp := &failingWriteSeekCloser{}
	shx := &seekTracker{Writer: buf}
	w, err := CreateFromWriteSeekClosers(shp, shx, nil, POINT)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(&Point{1, 2}); err == nil {
		t.Fatal("expected error from Write")
	}
}

func TestWriteZip(t *testing.T) {
	dir, err := os.MkdirTemp("", "go-shp-ziptest-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	zipPath := filepath.Join(dir, "out.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	layers := []Layer{
		{
			Base: "sites",
			Type: POINT,
			Populate: func(w *Writer) error {
				nameField, err := StringField("NAME", 20)
				if err != nil {
					return err
				}
				if err := w.SetFields([]Field{nameField}); err != nil {
					return err
				}
				if _, err := w.Write(&Point{10, 20}); err != nil {
					return err
				}
				return w.WriteAttribute(0, 0, "first")
			},
		},
		{
			Base: "pools",
			Type: POLYGON,
			Populate: func(w *Writer) error {
				w.SetProjection("WGS84")
				ring := []Point{{0, 0}, {0, 1}, {1, 1}, {1, 0}}
				_, err := w.Write(NewPolygonFromRings([][]Point{ring}))
				return err
			},
		},
	}
	if err := WriteZip(f, layers...); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	zr, err := OpenShapeFromZip(zipPath, "sites.shp")
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if !zr.Next() {
		t.Fatal("expected a shape in sites layer")
	}
	_, shape := zr.Shape()
	if _, ok := shape.(*Point); !ok {
		t.Fatalf("sites shape type = %T, want *Point", shape)
	}
	if fields := zr.Fields(); len(fields) != 1 || fields[0].String() != "NAME" {
		t.Fatalf("sites fields = %v, want [NAME]", fields)
	}

	zr2, err := OpenShapeFromZip(zipPath, "pools.shp")
	if err != nil {
		t.Fatal(err)
	}
	defer zr2.Close()
	if !zr2.Next() {
		t.Fatal("expected a shape in pools layer")
	}
	_, shape2 := zr2.Shape()
	if _, ok := shape2.(*Polygon); !ok {
		t.Fatalf("pools shape type = %T, want *Polygon", shape2)
	}
}

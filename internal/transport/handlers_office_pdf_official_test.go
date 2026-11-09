package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeOfficePDF struct{ err error }

func (f fakeOfficePDF) Generation(context.Context) (string, error) { return "gen-1", f.err }
func (f fakeOfficePDF) Render(context.Context, string, string, string) (OfficialOfficePDFResult, error) {
	n := 8
	return OfficialOfficePDFResult{MissingFonts: []string{}, Generation: "gen-1", Data: []byte("%PDF-1.7"), Eof: true, AbsolutePath: "x.docx", Version: "v1", Bytes: &n}, f.err
}

func TestOfficialOfficePDFHTTP(t *testing.T) {
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.AttachOfficialOfficePDFAPI(fakeOfficePDF{})
	t.Cleanup(s.DetachOfficialOfficePDFAPI)
	req := httptest.NewRequest(http.MethodPost, "/api/officeToPdf.generation", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.ServeOfficialOfficePDFAPI(rec, req, "officeToPdf.generation")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "gen-1") {
		t.Fatalf("generation = %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/officeToPdf.render", strings.NewReader(`{"workspaceFileScopeId":"s","path":"x.docx","priority":"foreground"}`))
	rec = httptest.NewRecorder()
	s.ServeOfficialOfficePDFAPI(rec, req, "officeToPdf.render")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "JVBERi0") {
		t.Fatalf("render = %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/officeToPdf.render", strings.NewReader(`{"workspaceFileScopeId":"s","path":"x.docx","priority":"now"}`))
	rec = httptest.NewRecorder()
	s.ServeOfficialOfficePDFAPI(rec, req, "officeToPdf.render")
	if rec.Code != 400 {
		t.Fatalf("bad priority = %d %s", rec.Code, rec.Body.String())
	}
	empty, _ := NewServer(Options{})
	req = httptest.NewRequest(http.MethodPost, "/api/officeToPdf.render", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	empty.ServeOfficialOfficePDFAPI(rec, req, "officeToPdf.render")
	if rec.Code != 400 {
		t.Fatalf("missing params = %d %s", rec.Code, rec.Body.String())
	}
}

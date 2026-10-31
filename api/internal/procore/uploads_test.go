package procore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCreateUpload_PostsToV11WithCompanyHeader(t *testing.T) {
	var gotMethod, gotAuth, gotCompany string
	var gotBody map[string]any
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		"/rest/v1.1/projects/42/uploads": func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotAuth, gotCompany = r.Method, r.Header.Get("Authorization"), r.Header.Get("Procore-Company-Id")
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode body: %v", err)
			}
			writeJSON(w, 201, `{"uuid":"01HUP","url":"https://s3.example.com/bucket","fields":{"key":"uploads/01HUP","Content-Type":"image/jpeg","policy":"p","x-amz-signature":"sig"}}`)
		},
	})
	c := newTestClient(f, &sleepRecorder{})

	up, err := c.CreateUpload(context.Background(), "tok", 7, 42, UploadCreate{ResponseFilename: "a.jpg", ResponseContentType: "image/jpeg"})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if gotMethod != http.MethodPost || gotAuth != "Bearer tok" || gotCompany != "7" {
		t.Errorf("method=%q auth=%q company=%q", gotMethod, gotAuth, gotCompany)
	}
	wantBody := map[string]any{"response_filename": "a.jpg", "response_content_type": "image/jpeg"}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("body = %v, want %v", gotBody, wantBody)
	}
	want := Upload{
		UUID: "01HUP",
		URL:  "https://s3.example.com/bucket",
		Fields: map[string]string{
			"key": "uploads/01HUP", "Content-Type": "image/jpeg", "policy": "p", "x-amz-signature": "sig",
		},
	}
	if !reflect.DeepEqual(up, want) {
		t.Errorf("upload = %+v, want %+v", up, want)
	}
}

// storagePart is one part of a multipart form the fake storage service read.
type storagePart struct {
	Name, Filename, ContentType, Value string
}

func TestPostUpload_MultipartFieldsThenFileLast_NoAuth(t *testing.T) {
	var (
		gotHeader        http.Header
		gotContentLength int64
		gotTE            []string
		parts            []storagePart
		readBytes        int64
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotContentLength, gotTE = r.ContentLength, r.TransferEncoding
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "multipart/form-data" {
			t.Errorf("content type %q: %v", r.Header.Get("Content-Type"), err)
			w.WriteHeader(400)
			return
		}
		cr := &countingReader{r: r.Body}
		mr := multipart.NewReader(cr, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("next part: %v", err)
				w.WriteHeader(400)
				return
			}
			b, _ := io.ReadAll(p)
			parts = append(parts, storagePart{Name: p.FormName(), Filename: p.FileName(), ContentType: p.Header.Get("Content-Type"), Value: string(b)})
		}
		readBytes = cr.n
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient("http://unused.invalid", srv.Client(), nil, nil)

	fields := map[string]string{
		"key":              "uploads/01HUP",
		"Content-Type":     "image/jpeg",
		"policy":           "cG9saWN5",
		"x-amz-credential": "AKIA/20261030/us-east-1/s3/aws4_request",
		"x-amz-algorithm":  "AWS4-HMAC-SHA256",
		"x-amz-date":       "20261030T000000Z",
		"x-amz-signature":  "abc123",
	}
	photo := strings.Repeat("J", 70_000)
	up := Upload{UUID: "01HUP", URL: srv.URL + "/bucket", Fields: fields}
	if err := c.PostUpload(context.Background(), up, "IMG_0001.jpg", int64(len(photo)), strings.NewReader(photo)); err != nil {
		t.Fatalf("PostUpload: %v", err)
	}

	if len(parts) != len(fields)+1 {
		t.Fatalf("got %d parts, want %d: %+v", len(parts), len(fields)+1, parts)
	}
	for _, p := range parts[:len(parts)-1] {
		want, ok := fields[p.Name]
		if !ok {
			t.Errorf("unexpected field %q", p.Name)
			continue
		}
		if p.Value != want || p.Filename != "" {
			t.Errorf("field %q = %q (filename %q), want %q", p.Name, p.Value, p.Filename, want)
		}
		delete(fields, p.Name)
	}
	if len(fields) != 0 {
		t.Errorf("fields not sent: %v", fields)
	}
	last := parts[len(parts)-1]
	if last.Name != "file" || last.Filename != "IMG_0001.jpg" || last.ContentType != "image/jpeg" {
		t.Errorf("last part name=%q filename=%q content type=%q", last.Name, last.Filename, last.ContentType)
	}
	if last.Value != photo {
		t.Errorf("file part has %d bytes, want %d", len(last.Value), len(photo))
	}
	for _, h := range []string{"Authorization", "Procore-Company-Id"} {
		if v := gotHeader.Get(h); v != "" {
			t.Errorf("storage request carried %s: %q", h, v)
		}
	}
	for k := range gotHeader {
		if strings.HasPrefix(strings.ToLower(k), "procore-") {
			t.Errorf("storage request carried Procore header %q", k)
		}
	}
	// S3 refuses a chunked POST, so the length must be declared up front.
	if gotContentLength != readBytes || len(gotTE) != 0 {
		t.Errorf("content length %d, body %d bytes, transfer encoding %v", gotContentLength, readBytes, gotTE)
	}
}

func TestPostUpload_Non2xx_APIError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code>" + strings.Repeat("x", 500) + "</Error>"))
	}))
	defer srv.Close()
	c := NewClient("http://unused.invalid", srv.Client(), nil, nil)

	up := Upload{UUID: "u", URL: srv.URL + "/bucket", Fields: map[string]string{"key": "k"}}
	err := c.PostUpload(context.Background(), up, "a.jpg", 3, strings.NewReader("abc"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", apiErr.Status)
	}
	if !strings.HasPrefix(apiErr.Body, "<Error><Code>AccessDenied</Code>") || len(apiErr.Body) != maxErrBody {
		t.Errorf("body (%d bytes) = %q", len(apiErr.Body), apiErr.Body)
	}
}

func TestPostUpload_HTTPRedirect_Refused(t *testing.T) {
	var followed bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bucket":
			_, _ = io.Copy(io.Discard, r.Body)
			http.Redirect(w, r, "http://"+r.Host+"/elsewhere", http.StatusFound)
		default:
			followed = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := NewClient("http://unused.invalid", srv.Client(), nil, nil)

	up := Upload{UUID: "u", URL: srv.URL + "/bucket", Fields: map[string]string{"key": "k"}}
	err := c.PostUpload(context.Background(), up, "a.jpg", 3, strings.NewReader("abc"))
	if err == nil {
		t.Fatal("redirect to http was followed")
	}
	if !errors.Is(err, errInsecureRedirect) || !errors.Is(err, ErrUnavailable) {
		t.Errorf("error %v, want errInsecureRedirect wrapped as ErrUnavailable", err)
	}
	if followed {
		t.Error("the http redirect target was requested")
	}
}

// A filename cannot break out of the file part's Content-Disposition header.
func TestPostUpload_FilenameControlCharsStripped(t *testing.T) {
	var gotFilename, gotDisposition string
	var gotInjected bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				gotFilename, gotDisposition = p.FileName(), p.Header.Get("Content-Disposition")
				gotInjected = p.Header.Get("X-Injected") != ""
			}
			_, _ = io.Copy(io.Discard, p)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient("http://unused.invalid", srv.Client(), nil, nil)

	up := Upload{UUID: "u", URL: srv.URL + "/bucket", Fields: map[string]string{"key": "k"}}
	name := "IMG\r\nX-Injected: 1\x00\t\x7f_0001.jpg"
	if err := c.PostUpload(context.Background(), up, name, 3, strings.NewReader("abc")); err != nil {
		t.Fatalf("PostUpload: %v", err)
	}
	if gotFilename != "IMGX-Injected: 1_0001.jpg" || gotInjected {
		t.Errorf("filename = %q (disposition %q), injected header %v", gotFilename, gotDisposition, gotInjected)
	}
}

// A storage URL that is not https is refused before anything is sent.
func TestPostUpload_NonHTTPSURL_Refused(t *testing.T) {
	c := NewClient("http://unused.invalid", nil, nil, nil)
	for _, raw := range []string{"http://storage.example.com/bucket", "ftp://storage.example.com/bucket", "//storage.example.com/bucket", "::bad"} {
		r := &countingReader{r: strings.NewReader("abc")}
		err := c.PostUpload(context.Background(), Upload{UUID: "u", URL: raw}, "a.jpg", 3, r)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%q: error %v, want ErrUnavailable", raw, err)
		}
		if r.n != 0 {
			t.Errorf("%q: read %d bytes of the body, want none", raw, r.n)
		}
	}
	err := c.PostUpload(context.Background(), Upload{UUID: "u", URL: "http://storage.example.com/bucket"}, "a.jpg", 3, strings.NewReader("abc"))
	if err == nil || !strings.Contains(err.Error(), `"http"`) {
		t.Errorf("error %v, want it to name the scheme", err)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

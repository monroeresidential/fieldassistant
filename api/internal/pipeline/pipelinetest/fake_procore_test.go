package pipelinetest

import (
	"context"
	"strings"
	"testing"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

func TestFakePostUpload_SizeMismatch_Errors(t *testing.T) {
	up := procore.Upload{UUID: "u1", URL: "https://storage.example.com/bucket"}
	for _, tc := range []struct {
		name    string
		size    int64
		wantErr bool
	}{
		{"exact", 3, false},
		{"short body", 4, true},
		{"long body", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFakeProcore()
			ran := false
			f.OnPostUpload = func() { ran = true }
			err := f.PostUpload(context.Background(), up, "a.jpg", tc.size, strings.NewReader("abc"))
			if (err != nil) != tc.wantErr {
				t.Fatalf("PostUpload = %v, want error %v", err, tc.wantErr)
			}
			if ran == tc.wantErr {
				t.Errorf("OnPostUpload ran = %v, want %v", ran, !tc.wantErr)
			}
			if len(f.Posted) != 1 || f.Posted[0].Bytes != 3 {
				t.Errorf("posted = %+v, want one record of 3 bytes", f.Posted)
			}
		})
	}
}

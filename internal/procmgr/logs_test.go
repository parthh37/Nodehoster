package procmgr

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestLogSinkClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "site.log")
	s := NewLogSink(path, 1, 1, 1)
	defer s.Close()
	// Nothing written yet: there is no file, and clearing is not an error.
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear without a log file: %v", err)
	}
	s.Write(model.LogLine{Time: time.Now(), Stream: "stdout", Text: "one"})
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Size() != 0 {
		t.Fatalf("log file after Clear: %v %v", st, err)
	}
	// Writing goes on in the same file.
	s.Write(model.LogLine{Time: time.Now(), Stream: "stdout", Text: "two"})
	if data, _ := os.ReadFile(path); len(data) == 0 || len(s.Recent(0)) != 1 {
		t.Fatalf("after Clear: file %q, %d recent lines", data, len(s.Recent(0)))
	}
}

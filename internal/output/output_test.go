package output

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Each automation keeps its newest Keep captures; another automation's are
// not counted against it.
func TestSaveKeepsTheNewestPerAutomation(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	base := time.Date(2026, 10, 24, 9, 0, 0, 0, time.Local).UnixNano()
	id := func(name string, i int) string { return fmt.Sprintf("%s-%d", name, base+int64(i)*int64(time.Minute)) }
	for i := range Keep + 5 {
		if err := Save(id("[KAN] Mails -> Kaneo", i), fmt.Sprintf("screen %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(id("other", 0), "other screen"); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(os.Getenv("HERDR_PLUGIN_STATE_DIR"), "output", "*.txt"))
	if len(files) != Keep+1 {
		t.Fatalf("%d files kept, want %d", len(files), Keep+1)
	}
	if Exists(id("[KAN] Mails -> Kaneo", 4)) || !Exists(id("[KAN] Mails -> Kaneo", 5)) || !Exists(id("other", 0)) {
		t.Error("pruned the wrong captures")
	}
	c, ok := Read(id("[KAN] Mails -> Kaneo", Keep+4))
	if !ok || c.Text != fmt.Sprintf("screen %d", Keep+4) || time.Since(c.At) > time.Minute {
		t.Errorf("Read = %+v, %v", c, ok)
	}
	if _, ok := Read(id("never ran", 0)); ok {
		t.Error("a run without a capture has one")
	}
}

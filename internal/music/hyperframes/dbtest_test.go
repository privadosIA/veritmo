package hyperframes

import (
	"testing"

	"veritmo/internal/platform/store"
)

// eachStore runs a test twice: on a store that keeps the record of a block in its block.json and on one that keeps it in the database, so that
// the two behave the same. newStore makes a new, empty store of that kind (its files in a folder of their own).
func eachStore(t *testing.T, run func(t *testing.T, newStore func() Store)) {
	t.Helper()
	t.Run("files", func(t *testing.T) { run(t, func() Store { return Store{Dir: t.TempDir()} }) })
	t.Run("database", func(t *testing.T) {
		run(t, func() Store {
			reg := store.NewRegistry()
			RegisterStore(reg, t.TempDir())
			st, err := store.OpenMemory(reg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.Migrate(); err != nil {
				t.Fatal(err)
			}
			return NewStoreDB(t.TempDir(), st)
		})
	})
}

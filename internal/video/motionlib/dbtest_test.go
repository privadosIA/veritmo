package motionlib

import (
	"testing"

	"veritmo/internal/platform/store"
)

// eachStore runs a test twice: on a library that keeps the record of a motion in its motion.json and on one that keeps it in the database, so that
// the two behave the same. newStore makes an empty library of that kind whose motions are under dir.
func eachStore(t *testing.T, run func(t *testing.T, newStore func(dir string) *Store)) {
	t.Helper()
	t.Run("files", func(t *testing.T) { run(t, NewStore) })
	t.Run("database", func(t *testing.T) {
		run(t, func(dir string) *Store {
			reg := store.NewRegistry()
			RegisterStore(reg, dir)
			st, err := store.OpenMemory(reg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.Migrate(); err != nil {
				t.Fatal(err)
			}
			return NewStoreDB(dir, st)
		})
	})
}

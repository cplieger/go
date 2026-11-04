package keylock

// Entries reports how many keys have an entry, held or waited on.
func Entries[K comparable](s *Set[K]) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

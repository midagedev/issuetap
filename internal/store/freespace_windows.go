package store

// Windows has no Statfs in the standard library and this check is advisory
// (the migration is atomic either way: the transaction commits or the file
// stays v1). Skip it rather than take a dependency for a warning.
func requireFreeSpace(path string, need int64) error { return nil }

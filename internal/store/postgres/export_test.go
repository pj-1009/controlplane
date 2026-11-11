package postgres

// InsertTaskWithOutbox exposes insertTaskWithOutbox to the external tests,
// which need it to check that a rolled-back transaction leaves no row.
var InsertTaskWithOutbox = insertTaskWithOutbox

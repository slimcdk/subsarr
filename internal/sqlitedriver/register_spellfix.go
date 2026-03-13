package sqlitedriver

// Register spellfix1 as a SQLite auto-extension so it is available on every
// database connection.
//
// CGO_CFLAGS must point at the mattn/go-sqlite3 module directory so the
// compiler can find sqlite3-binding.h and sqlite3ext.h at build time.
// The Makefile and Dockerfile set this automatically.
//
// spellfix.c is compiled alongside this file automatically by CGO since it
// lives in the same package directory.

/*
#include "sqlite3-binding.h"

// Forward-declare the spellfix1 entry point (defined in spellfix.c).
typedef struct sqlite3_api_routines sqlite3_api_routines;
extern int sqlite3_spellfix_init(sqlite3 *db, char **pzErrMsg, const sqlite3_api_routines *pApi);

static void subsarr_register_spellfix(void) {
    sqlite3_auto_extension((void(*)(void))sqlite3_spellfix_init);
}
*/
import "C"

func init() {
	C.subsarr_register_spellfix()
}

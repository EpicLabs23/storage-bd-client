// Command fakestorage runs the test fake of storage.bd for manual UI work:
//
//	go run ./internal/testutil/cmd/fakestorage
//	make build API_URL=<printed URL> && ./bin/storagebd --home /tmp/sbui serve
//
// Account login: any email, password testutil.FakeAccountPassword.
// Data is in memory only. Dev use only; never shipped.
package main

import (
	"fmt"
	"os"
	"os/signal"

	"storage-bd-client/internal/testutil"
)

func main() {
	f := testutil.StartFakeStorage()
	defer f.Close()
	fmt.Println(f.API.URL)
	fmt.Fprintf(os.Stderr, "fake storage.bd at %s (login password %q)\n", f.API.URL, testutil.FakeAccountPassword)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

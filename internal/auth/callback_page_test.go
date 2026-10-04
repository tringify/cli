package auth

import (
	"strings"
	"testing"
)

func TestCallbackPageEscapesTheMessageAndLoadsNothing(t *testing.T) {
	page := callbackPage(false, "Sign-in did not complete", `sign-in failed: <script>alert(1)</script>`)
	if strings.Contains(page, "<script>") || !strings.Contains(page, "&lt;script&gt;") {
		t.Fatal("error text was not escaped")
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") || strings.Contains(page, "src=") {
		t.Fatal("callback page must not load anything from the network")
	}
	if !strings.Contains(callbackPage(true, "You're signed in", "Go back"), "You&#39;re signed in") {
		t.Fatal("success title missing")
	}
}

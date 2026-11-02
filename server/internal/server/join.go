package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"

	"github.com/labstack/echo/v5"
)

// The /join page (E2EE §6.1): an invite link is `<server>/join#<s>`, and
// this page hands it to the installed app as `runer://join?host=<server>#<s>`.
// It is static: the secret is in the URL fragment, which browsers never
// send, so the server learns nothing from serving it.

const joinStyle = `body{font:16px/1.5 system-ui,sans-serif;max-width:28rem;margin:15vh auto;padding:0 1.25rem;color:#1c1c1e;background:#fff}
h1{font-size:1.4rem}
a.open{display:inline-block;margin:.5rem 0 1rem;padding:.6rem 1rem;border-radius:.6rem;background:#1c1c1e;color:#fff;text-decoration:none}
p.note{color:#6b6b70;font-size:.9rem}
@media (prefers-color-scheme:dark){body{color:#f2f2f3;background:#161618}a.open{background:#f2f2f3;color:#161618}p.note{color:#9a9aa0}}`

const joinScript = `if (location.hash.length > 1) {
  var base = location.origin + location.pathname.replace(/\/join\/?$/, '');
  var link = 'runer://join?host=' + encodeURIComponent(base) + location.hash;
  document.getElementById('open').href = link;
  location.replace(link);
} else {
  document.getElementById('invite').hidden = true;
  document.getElementById('incomplete').hidden = false;
}`

const joinPage = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Join a list</title>
<style>` + joinStyle + `</style>
<h1>You're invited to a list</h1>
<div id="invite">
<p>The invite opens in the app.</p>
<a class="open" id="open" href="#">Open the invite</a>
<p class="note">If nothing happens, copy this page's address and paste it in the app where you add a list. No app on this device yet? Install it, sign in, and open this link again.</p>
</div>
<p id="incomplete" hidden>This invite link is incomplete. Ask the person who sent it for the whole link.</p>
<script>` + joinScript + `</script>
`

// joinCSP allows exactly the page's own inline style and script, by hash.
var joinCSP = "default-src 'none'; style-src '" + cspHash(joinStyle) + "'; script-src '" + cspHash(joinScript) + "'; frame-ancestors 'none'"

func cspHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

func joinHandler(c *echo.Context) error {
	c.Response().Header().Set("Content-Security-Policy", joinCSP)
	return c.HTML(http.StatusOK, joinPage)
}

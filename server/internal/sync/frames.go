package sync

// Wire frame shapes, Sync §3. Every frame is `{"t": "...",
// ...}`. header/nonce/ct/sig are base64url strings: header is the base64url
// of the exact header bytes the author signed, so the server can relay and
// store all four without ever re-encoding signed content.

type clientFrame struct {
	T      string    `json:"t"`
	Cid    string    `json:"cid,omitempty"`
	Lists  []subList `json:"lists,omitempty"`
	Header string    `json:"header,omitempty"`
	Nonce  string    `json:"nonce,omitempty"`
	CT     string    `json:"ct,omitempty"`
	Sig    string    `json:"sig,omitempty"`
}

type subList struct {
	List  string `json:"list"`
	Since int64  `json:"since"`
}

// updateHeader is the signed header (E2EE §7), in its one canonical
// encoding: these keys, in this order, no whitespace. The server parses it
// only for routing and authorization. Type is "update" or "snapshot" and
// must match the frame; CoversSeq is present on snapshots only, and being
// signed, a server can't misstate a snapshot's coverage.
type updateHeader struct {
	Type      string `json:"type"`
	List      string `json:"list"`
	Epoch     int64  `json:"epoch"`
	CoversSeq *int64 `json:"covers_seq,omitempty"`
	Author    string `json:"author"` // identity_sign_pub, base64url
	LogHead   string `json:"log_head"`
}

// relayedMsg is the {header, nonce, ct, sig} object server->client update
// and snapshot frames carry (Sync §3: "msg is the original
// {header, nonce, ct, sig}"). All four are the strings the author sent.
type relayedMsg struct {
	Header string `json:"header"`
	Nonce  string `json:"nonce"`
	CT     string `json:"ct"`
	Sig    string `json:"sig"`
}

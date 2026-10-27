package core

// NeutralMasquerade is what a Hysteria2 inbound answers a plain HTTP/3 probe
// with: a stock nginx 404, the most common thing on the internet. It replaces
// an earlier page titled with the product name, which let anyone fingerprint
// an EdgeNest node with a single request.
const (
	NeutralMasquerade       = "<html>\r\n<head><title>404 Not Found</title></head>\r\n<body>\r\n<center><h1>404 Not Found</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"
	NeutralMasqueradeStatus = 404
)

// LegacyMasquerade is the old product-named page; renderers swap it for
// NeutralMasquerade so nodes created before the change stop advertising it
// without a database migration.
const LegacyMasquerade = "<!doctype html><title>EdgeNest</title>\n"

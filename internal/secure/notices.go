package secure

import _ "embed"

// ThirdPartyNotices travels inside the executable as well as the release folder.
//
//go:embed THIRD-PARTY-NOTICES.txt
var ThirdPartyNotices string

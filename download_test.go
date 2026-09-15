//go:build windows

package main

import "testing"

// Sample of apachelounge.com/download/ — trimmed to the shapes that matter.
const apachePageSample = `
<a href="/download/VS17/">VS17</a>
<a href="/download/VS18/binaries/httpd-2.4.67-260504-Win64-VS18.zip">old</a>
<a href="/download/VS18/binaries/httpd-2.4.67-260504-Win64-VS18.zip.asc">sig</a>
<a href="/download/VS18/binaries/httpd-2.4.68-260617-Win64-VS18.zip">mid</a>
<a href="/download/VS18/binaries/httpd-2.4.68-260827-Win64-VS18.zip">new</a>
<a href="/download/VS18/binaries/httpd-2.4.68-260827-win32-vs18.zip">32-bit, not win64</a>
<a href="/download/VS18/modules/mod_fcgid-2.3.10-win64-VS18.zip">not httpd</a>
`

func TestPickNewestApacheBuild(t *testing.T) {
	link, ver, build, vs, ok := pickNewestApacheBuild(apachePageSample)
	if !ok {
		t.Fatal("no build found")
	}
	if link != "/download/VS18/binaries/httpd-2.4.68-260827-Win64-VS18.zip" {
		t.Errorf("link = %q", link)
	}
	if ver != "2.4.68" || build != "260827" || vs != "18" {
		t.Errorf("got ver=%q build=%q vs=%q", ver, build, vs)
	}
}

func TestPickNewestApacheBuildEmpty(t *testing.T) {
	if _, _, _, _, ok := pickNewestApacheBuild("<html>nothing here</html>"); ok {
		t.Error("expected no match")
	}
}

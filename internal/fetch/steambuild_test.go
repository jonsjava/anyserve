package fetch

import (
	"os"
	"path/filepath"
	"testing"
)

const appInfo = `Connecting anonymously to Steam Public...OK
AppID : 258550, change number : 1/0, last change : Thu Jan  1 00:00:00 1970
"258550"
{
	"common"
	{
		"name"		"Rust Dedicated Server"
	}
	"depots"
	{
		"258551"
		{
			"name"		"Rust Dedicated - Linux64"
		}
		"branches"
		{
			"public"
			{
				"buildid"		"20512345"
				"timeupdated"		"1763000000"
			}
			"staging"
			{
				"buildid"		"20599999"
				"description"		"Staging \"test\" branch"
			}
		}
	}
}
`

func TestBranchBuild(t *testing.T) {
	if got := branchBuild(appInfo, "public"); got != "20512345" {
		t.Errorf("public = %q", got)
	}
	if got := branchBuild(appInfo, "staging"); got != "20599999" {
		t.Errorf("staging = %q", got)
	}
	if got := branchBuild(appInfo, "nope"); got != "" {
		t.Errorf("missing branch = %q", got)
	}
	if got := branchBuild("garbage", "public"); got != "" {
		t.Errorf("garbage = %q", got)
	}
}

func TestInstalledBuild(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "steamapps"), 0o755)
	os.WriteFile(filepath.Join(dir, "steamapps", "appmanifest_258550.acf"), []byte("\"AppState\"\n{\n\t\"appid\"\t\t\"258550\"\n\t\"buildid\"\t\t\"20512345\"\n}\n"), 0o644)
	if got := InstalledBuild(dir, 258550); got != "20512345" {
		t.Errorf("got %q", got)
	}
	if got := InstalledBuild(dir, 1); got != "" {
		t.Errorf("missing = %q", got)
	}
}

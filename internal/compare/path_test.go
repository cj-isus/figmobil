package compare

import "testing"

func TestSkippedSubtreesRespectWindowsCaseAndPathBoundaries(t *testing.T) {
	tree := snapshot{blocked: []string{"Foo"}}
	for _, path := range []string{"Foo", "foo", "foo/child.txt", "FOO/CHILD/inner.txt"} {
		if !tree.isBlocked(path) {
			t.Errorf("unknown subtree classified as absent: %s", path)
		}
	}
	for _, path := range []string{"FooBar/child.txt", "other/Foo/child.txt", "other.txt"} {
		if tree.isBlocked(path) {
			t.Errorf("unrelated path blocked: %s", path)
		}
	}
}

package cli

import "testing"

func TestStableCommandTree(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"install"}, {"upgrade"}, {"config", "generate"}, {"config", "client"}, {"service"}} {
		cmd, remaining, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if len(remaining) != 0 {
			t.Fatalf("find %v left %v", args, remaining)
		}
		if cmd.Name() != args[len(args)-1] {
			t.Fatalf("find %v got %s", args, cmd.Name())
		}
	}
}

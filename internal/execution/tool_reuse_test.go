package execution

import "testing"

func TestToolArgumentIdentityPreservesLargeIntegers(t *testing.T) {
	if !sameToolArguments(`{"a":1,"b":{"x":2}}`, `{ "b": {"x":2}, "a":1 }`) {
		t.Fatal("equivalent object order should match")
	}
	if sameToolArguments(`{"id":9007199254740992}`, `{"id":9007199254740993}`) {
		t.Fatal("large integers collapsed into the same invocation identity")
	}
	if sameToolArguments(`{"path":"a"}`, `{"path":"b"}`) {
		t.Fatal("different tool arguments matched")
	}
}

package kernel

import (
	"reflect"
	"testing"

	"github.com/cedar2025/xboard-node/internal/model"
)

func TestUserDiff(t *testing.T) {
	old := model.UserSpec{ID: 1, UUID: "old"}
	updated := model.UserSpec{ID: 1, UUID: "new"}
	other := model.UserSpec{ID: 2, UUID: "other"}
	limited := old
	limited.SpeedLimit = 10
	cases := []struct {
		name                  string
		old, new, add, remove []model.UserSpec
	}{
		{"replace UUID", []model.UserSpec{old}, []model.UserSpec{updated}, []model.UserSpec{updated}, []model.UserSpec{old}},
		{"add", []model.UserSpec{old}, []model.UserSpec{old, other}, []model.UserSpec{other}, nil},
		{"remove", []model.UserSpec{old, other}, []model.UserSpec{old}, nil, []model.UserSpec{other}},
		{"limits only", []model.UserSpec{old}, []model.UserSpec{limited}, nil, nil},
		{"empty", nil, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			add, remove := UserDiff(tc.old, tc.new)
			if !reflect.DeepEqual(add, tc.add) || !reflect.DeepEqual(remove, tc.remove) {
				t.Fatalf("UserDiff() = (%v, %v), want (%v, %v)", add, remove, tc.add, tc.remove)
			}
		})
	}
}

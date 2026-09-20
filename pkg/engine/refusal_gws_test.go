package engine

import (
	"fmt"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/state"
)

func TestConflictsRefusalsBounded(t *testing.T) {
	Convey("Given a fresh state", t, func() {
		st := state.New()

		Convey("When more refusals than the bound are recorded", func() {
			for i := range state.MaxRefusals + 10 {
				st.AddRefusal(state.Refusal{ID: fmt.Sprintf("id-%02d", i), Code: state.RefusalStaleConflict})
			}

			Convey("Then only the last MaxRefusals remain, oldest dropped", func() {
				So(st.Refusals, ShouldHaveLength, state.MaxRefusals)
				So(st.Refusals[0].ID, ShouldEqual, "id-10")

				last, ok := st.LastRefusal()
				So(ok, ShouldBeTrue)
				So(last.ID, ShouldEqual, fmt.Sprintf("id-%02d", state.MaxRefusals+9))
			})
		})
	})
}

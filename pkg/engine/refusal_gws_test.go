package engine_test

import (
	"errors"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

// TestRefusalErrorIsNamedAndCarriesItsCode pins what an embedder needs: a
// refusal is classifiable by type and names its machine code, and a refusal
// raised by a caller travels the same way as one beadle raised itself.
func TestRefusalErrorIsNamedAndCarriesItsCode(t *testing.T) {
	Convey("Given a refusal beadle raised", t, func() {
		err := engine.NewRefusalError("risky-change", "the change alters an MCP command")

		Convey("Then it is recognised as a refusal and carries its code", func() {
			refused, ok := engine.IsRefusal(err)
			So(ok, ShouldBeTrue)
			So(refused.Code(), ShouldEqual, "risky-change")
			So(refused.Message(), ShouldEqual, "the change alters an MCP command")
			So(err.Error(), ShouldEqual, "risky-change: the change alters an MCP command")
		})

		Convey("Then it is found through a wrapping chain", func() {
			wrapped := errors.Join(errors.New("resolve conflicts"), err)

			_, ok := engine.IsRefusal(wrapped)
			So(ok, ShouldBeTrue)
		})
	})

	Convey("Given an ordinary error", t, func() {
		Convey("Then it is not a refusal and carries no code", func() {
			_, ok := engine.IsRefusal(errors.New("disk on fire"))
			So(ok, ShouldBeFalse)
		})
	})
}

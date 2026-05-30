package billing

import "testing"

func TestCaptureDeltas(t *testing.T) {
	cases := []struct {
		name            string
		res             Reservation
		captured        int
		wantRelIncluded int
		wantRelTopup    int
		wantExtra       int
	}{
		{
			name:            "exact match no release",
			res:             Reservation{ReservedCredits: 4, FromIncludedCredits: 2, FromTopupCredits: 2},
			captured:        4,
			wantRelIncluded: 0,
			wantRelTopup:    0,
			wantExtra:       0,
		},
		{
			name:            "less than reserved releases remainder",
			res:             Reservation{ReservedCredits: 4, FromIncludedCredits: 2, FromTopupCredits: 2},
			captured:        1,
			wantRelIncluded: 1,
			wantRelTopup:    2,
			wantExtra:       0,
		},
		{
			name:            "zero captured releases everything",
			res:             Reservation{ReservedCredits: 4, FromIncludedCredits: 2, FromTopupCredits: 2},
			captured:        0,
			wantRelIncluded: 2,
			wantRelTopup:    2,
			wantExtra:       0,
		},
		{
			name:            "negative captured treated as zero",
			res:             Reservation{ReservedCredits: 4, FromIncludedCredits: 2, FromTopupCredits: 2},
			captured:        -1,
			wantRelIncluded: 2,
			wantRelTopup:    2,
			wantExtra:       0,
		},
		{
			name:            "more than reserved generates extra",
			res:             Reservation{ReservedCredits: 2, FromIncludedCredits: 1, FromTopupCredits: 1},
			captured:        5,
			wantRelIncluded: 0,
			wantRelTopup:    0,
			wantExtra:       3,
		},
		{
			name:            "included only reservation",
			res:             Reservation{ReservedCredits: 3, FromIncludedCredits: 3, FromTopupCredits: 0},
			captured:        2,
			wantRelIncluded: 1,
			wantRelTopup:    0,
			wantExtra:       0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relInc, relTop, extra := captureDeltas(tc.res, tc.captured)
			if relInc != tc.wantRelIncluded || relTop != tc.wantRelTopup || extra != tc.wantExtra {
				t.Fatalf("captureDeltas = (%d, %d, %d), want (%d, %d, %d)",
					relInc, relTop, extra,
					tc.wantRelIncluded, tc.wantRelTopup, tc.wantExtra)
			}
		})
	}
}

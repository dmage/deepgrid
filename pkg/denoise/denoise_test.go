package denoise

import "testing"

func TestDenoise(t *testing.T) {
	testCases := []struct {
		Input, Output string
	}{
		{
			Input:  "UID:\"df658a21-3a4b-4e02-9415-85fef7b8ea2c\"",
			Output: "UID:\"UUID\"",
		},
		{
			Input:  "Feb 15 00:36:57.939: INFO: Running 'oc --kubeconfig=/tmp/tmp.bjZYPSqRRL observe serviceaccounts --once'",
			Output: "Feb 0 0:0:0.RANDOM: INFO: Running 'oc -RANDOM=/tmp/tmp.RANDOM observe serviceaccounts -RANDOM'",
		},
		{
			Input:  "Feb 15 10:31:54.483 W ns/e2e-test-s2i-build-root-f2rcw buildconfig/nodejspass reason/BuildConfigTriggerFailed error triggering Build for BuildConfig e2e-test-s2i-build-root-f2rcw/nodejspass: Internal error occurred: build config e2e-test-s2i-build-root-f2rcw/nodejspass has already instantiated a build for imageid quay.io/openshift/community-e2e-images@sha256:8c2e8b2c36d1775e3d32f598fff3191bd50ef967c6ce7e600a01d609d7e4648e",
			Output: "Feb 0 0:0:0.RANDOM W ns/e0e-RANDOM buildconfig/nodejspass reason/BuildConfigTriggerFailed error triggering Build for BuildConfig e0e-RANDOM/nodejspass: Internal error occurred: build config e0e-RANDOM/nodejspass has already instantiated a build for imageid quay.RANDOM/openshift/community-RANDOM@sha0:HASH",
		},
	}
	for _, tc := range testCases {
		output := Denoise(tc.Input)
		if output != tc.Output {
			t.Errorf("Denoise(%q): got %q, want %q", tc.Input, output, tc.Output)
		}
	}
}

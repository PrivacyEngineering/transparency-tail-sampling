// Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.

package transparencyprocessor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Rate allocation: upstream gives unlisted sub-policies 0 spans/s; H-7b distributes the remainder.
func TestRateAllocationUnlisted(t *testing.T) {
	cfg := &CompositeCfg{
		MaxTotalSpansPerSecond: 1000,
		SubPolicyCfg: []CompositeSubPolicyCfg{
			{sharedPolicyCfg: sharedPolicyCfg{Name: "tilt"}}, {sharedPolicyCfg: sharedPolicyCfg{Name: "pd"}}, {sharedPolicyCfg: sharedPolicyCfg{Name: "always"}},
		},
		RateAllocation: []RateAllocationCfg{{Policy: "tilt", Percent: 50}, {Policy: "pd", Percent: 25}},
	}
	m := getRateAllocationMap(cfg)
	assert.Equal(t, 500.0, m["tilt"])
	assert.Equal(t, 250.0, m["pd"])
	assert.Equal(t, 0.0, m["always"], "thesis/upstream: always_sample fallback gets no budget")
	cfg.UnallocatedShareRemaining = true
	assert.Equal(t, 250.0, getRateAllocationMap(cfg)["always"])
}

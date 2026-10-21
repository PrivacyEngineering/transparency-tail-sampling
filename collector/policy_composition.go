package transparencyprocessor

import (
	"fmt"
	"github.com/akkbng/otel-transparency-collector/internal/sampling"
	"go.opentelemetry.io/collector/component"
)

func getNewCompositePolicy(settings component.TelemetrySettings, config *CompositeCfg) (sampling.PolicyEvaluator, error) {
	var subPolicyEvalParams []sampling.SubPolicyEvalParams
	rateAllocationsMap := getRateAllocationMap(config)
	unbudgeted := map[int]bool{}
	for i := range config.SubPolicyCfg {
		for _, n := range config.UnbudgetedPolicies {
			if n == config.SubPolicyCfg[i].Name {
				unbudgeted[i] = true
			}
		}
	}
	for i := range config.SubPolicyCfg {
		policyCfg := &config.SubPolicyCfg[i]
		policy, err := getCompositeSubPolicyEvaluator(settings, policyCfg)
		if err != nil {
			return nil, err
		}

		evalParams := sampling.SubPolicyEvalParams{
			Evaluator:         policy,
			MaxSpansPerSecond: int64(rateAllocationsMap[policyCfg.Name]),
		}
		subPolicyEvalParams = append(subPolicyEvalParams, evalParams)
	}
	return sampling.NewCompositeWithOptions(settings.Logger, config.MaxTotalSpansPerSecond, subPolicyEvalParams, sampling.MonotonicClock{},
		sampling.CompositeOptions{FallThrough: config.FallThrough, EnforceTotalBudget: config.EnforceTotalBudget, Unbudgeted: unbudgeted}), nil
}

// Apply rate allocations to the sub-policies
func getRateAllocationMap(config *CompositeCfg) map[string]float64 {
	rateAllocationsMap := make(map[string]float64)
	maxTotalSPS := float64(config.MaxTotalSpansPerSecond)
	// Default SPS determined by equally diving number of sub policies
	defaultSPS := maxTotalSPS / float64(len(config.SubPolicyCfg))
	for _, rAlloc := range config.RateAllocation {
		if rAlloc.Percent > 0 {
			rateAllocationsMap[rAlloc.Policy] = (float64(rAlloc.Percent) / 100) * maxTotalSPS
		} else {
			rateAllocationsMap[rAlloc.Policy] = defaultSPS
		}
	}
	// H-7b: sub-policies without a rate_allocation entry share the unallocated remainder (upstream: they get 0).
	if config.UnallocatedShareRemaining {
		allocated := 0.0
		var unlisted []string
		for _, sp := range config.SubPolicyCfg {
			if v, ok := rateAllocationsMap[sp.Name]; ok {
				allocated += v
			} else {
				unlisted = append(unlisted, sp.Name)
			}
		}
		if rem := maxTotalSPS - allocated; rem > 0 && len(unlisted) > 0 {
			for _, n := range unlisted {
				rateAllocationsMap[n] = rem / float64(len(unlisted))
			}
		}
	}
	return rateAllocationsMap
}

// Return instance of composite sub-policy
func getCompositeSubPolicyEvaluator(settings component.TelemetrySettings, cfg *CompositeSubPolicyCfg) (sampling.PolicyEvaluator, error) {
	switch cfg.Type {
	case And:
		return getNewAndPolicy(settings, &cfg.AndCfg)
	default:
		return getSharedPolicyEvaluator(settings, &cfg.sharedPolicyCfg)
	}
}

func getNewAndPolicy(settings component.TelemetrySettings, config *AndCfg) (sampling.PolicyEvaluator, error) {
	var subPolicyEvaluators []sampling.PolicyEvaluator
	for i := range config.SubPolicyCfg {
		policyCfg := &config.SubPolicyCfg[i]
		policy, err := getAndSubPolicyEvaluator(settings, policyCfg)
		if err != nil {
			return nil, err
		}
		subPolicyEvaluators = append(subPolicyEvaluators, policy)
	}
	return sampling.NewAnd(settings.Logger, subPolicyEvaluators), nil
}

// Return instance of and sub-policy
func getAndSubPolicyEvaluator(settings component.TelemetrySettings, cfg *AndSubPolicyCfg) (sampling.PolicyEvaluator, error) {
	return getSharedPolicyEvaluator(settings, &cfg.sharedPolicyCfg)
}

func getSharedPolicyEvaluator(settings component.TelemetrySettings, cfg *sharedPolicyCfg) (sampling.PolicyEvaluator, error) {
	switch cfg.Type {
	case AlwaysSample:
		return sampling.NewAlwaysSample(settings), nil
	case Probabilistic:
		pCfg := cfg.ProbabilisticCfg
		return sampling.NewProbabilisticSampler(settings, pCfg.HashSalt, pCfg.SamplingPercentage), nil
	case StringAttribute:
		safCfg := cfg.StringAttributeCfg
		return sampling.NewStringAttributeFilter(settings, safCfg.Key, safCfg.Values, safCfg.EnabledRegexMatching, safCfg.CacheMaxSize, safCfg.InvertMatch), nil
	case StatusCode:
		scfCfg := cfg.StatusCodeCfg
		return sampling.NewStatusCodeFilter(settings, scfCfg.StatusCodes)
	case RateLimiting:
		rlfCfg := cfg.RateLimitingCfg
		return sampling.NewRateLimiting(settings, rlfCfg.SpansPerSecond), nil
	case TraceState:
		tsfCfg := cfg.TraceStateCfg
		return sampling.NewTraceStateFilter(settings, tsfCfg.Key, tsfCfg.Values), nil
	case BooleanAttribute:
		bafCfg := cfg.BooleanAttributeCfg
		return sampling.NewBooleanAttributeFilter(settings, bafCfg.Key, bafCfg.Value), nil
	case OTTLCondition:
		ottlfCfg := cfg.OTTLConditionCfg
		return sampling.NewOTTLConditionFilter(settings, ottlfCfg.SpanConditions, ottlfCfg.SpanEventConditions, ottlfCfg.ErrorMode)
	case TransparencyAttribute:
		tafCfg := cfg.TransparencyAttributeCfg
		if tafCfg.Disabled {
			return sampling.NewDisabledPolicy(), nil
		}
		return sampling.NewTransparencyAttributeFilter(settings, tafCfg.TiltURL, tafCfg.IgnoreUnannotatedSpans)
	case TiltConformity:
		c := cfg.TiltConformityCfg
		return sampling.NewTiltConformity(settings, sampling.TiltConformityConfig{TiltURL: c.TiltURL, TiltSHA256: c.TiltSHA256,
			SampleOn: c.SampleOn, RefreshInterval: c.RefreshInterval, Disabled: c.Disabled}), nil

	default:
		return nil, fmt.Errorf("unknown sampling policy type %s", cfg.Type)
	}
}

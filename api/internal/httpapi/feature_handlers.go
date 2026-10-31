package httpapi

import "context"

// GetProjectFeatures reports which features the user may use on the
// project. The company must be visible to the user; Features checks the
// project with the user's token. Cache-Control: no-cache asks Procore again.
func (s *Server) GetProjectFeatures(ctx context.Context, req GetProjectFeaturesRequestObject) (GetProjectFeaturesResponseObject, error) {
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	force := req.Params.CacheControl != nil && hasNoCache(*req.Params.CacheControl)
	states, at, err := s.deps.Features.Get(ctx, userID, companyID, req.ProjectId, force)
	if err != nil {
		return nil, s.procoreFailure("features", err)
	}
	features := make([]FeatureState, 0, len(states))
	for _, st := range states {
		f := FeatureState{Feature: FeatureStateFeature(st.Feature), Available: st.Available}
		if !st.Available {
			f.Reason = &st.Reason
		}
		features = append(features, f)
	}
	return GetProjectFeatures200JSONResponse{Features: features, FetchedAt: at}, nil
}

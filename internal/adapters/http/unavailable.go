package httpapi

import "context"

// The generated interface covers the complete public contract. Until their
// vertical slices land, vault and secret operations return a declared 503 and
// are intentionally absent from service capabilities.

func (s *Server) ListVaults(ctx context.Context, _ ListVaultsRequestObject) (ListVaultsResponseObject, error) {
	return ListVaults503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) CreateVault(ctx context.Context, _ CreateVaultRequestObject) (CreateVaultResponseObject, error) {
	return CreateVault503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) DeleteVault(ctx context.Context, _ DeleteVaultRequestObject) (DeleteVaultResponseObject, error) {
	return DeleteVault503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) GetVault(ctx context.Context, _ GetVaultRequestObject) (GetVaultResponseObject, error) {
	return GetVault503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) UpdateVault(ctx context.Context, _ UpdateVaultRequestObject) (UpdateVaultResponseObject, error) {
	return UpdateVault503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) DeleteSecret(ctx context.Context, _ DeleteSecretRequestObject) (DeleteSecretResponseObject, error) {
	return DeleteSecret503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) GetSecret(ctx context.Context, _ GetSecretRequestObject) (GetSecretResponseObject, error) {
	return GetSecret503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) PutSecret(ctx context.Context, _ PutSecretRequestObject) (PutSecretResponseObject, error) {
	return PutSecret503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) GetSecretMetadata(ctx context.Context, _ GetSecretMetadataRequestObject) (GetSecretMetadataResponseObject, error) {
	return GetSecretMetadata503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

func (s *Server) ListSecrets(ctx context.Context, _ ListSecretsRequestObject) (ListSecretsResponseObject, error) {
	return ListSecrets503ApplicationProblemPlusJSONResponse{unavailableProblem(requestID(ctx))}, nil
}

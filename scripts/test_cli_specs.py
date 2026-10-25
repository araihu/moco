"""Regression tests for the boundary between the API contract and CLI metadata."""
import copy
import unittest

from check_cli_specs import validate


class CLIContractTests(unittest.TestCase):
    def setUp(self):
        self.base = {"openapi": "3.0.3", "security": [{"bearerAuth": []}],
                     "paths": {"/tenants": {"post": {"operationId": "createTenant", "responses": {"201": {"description": "Created"}}}}}}
        self.cli = copy.deepcopy(self.base)
        self.cli["paths"]["/tenants"]["post"].update({"x-cli-group": "tenants", "x-cli-name": "create"})

    def test_metadata_preserves_contract(self):
        self.assertEqual(validate(self.base, self.cli), 1)

    def test_contract_and_auth_changes_are_rejected(self):
        for field, value in (("security", []), ("openapi", "3.1.0")):
            with self.subTest(field=field):
                changed = copy.deepcopy(self.cli)
                changed[field] = value
                with self.assertRaises(ValueError):
                    validate(self.base, changed)
        self.cli["paths"]["/tenants"]["post"]["operationId"] = "renamed"
        with self.assertRaises(ValueError):
            validate(self.base, self.cli)

    def test_missing_metadata_and_duplicate_names_are_rejected(self):
        with self.assertRaises(ValueError):
            validate(self.base, self.base)
        self.base["paths"]["/tenants"]["get"] = {"operationId": "listTenants"}
        self.cli["paths"]["/tenants"]["get"] = {"operationId": "listTenants", "x-cli-group": "tenants", "x-cli-name": "create"}
        with self.assertRaises(ValueError):
            validate(self.base, self.cli)

    def test_base_and_misplaced_metadata_are_rejected(self):
        polluted = copy.deepcopy(self.base)
        polluted["x-cli-name"] = "bad"
        with self.assertRaises(ValueError):
            validate(polluted, self.cli)
        self.cli["x-cli-name"] = "bad"
        with self.assertRaises(ValueError):
            validate(self.base, self.cli)

    def test_invalid_names_are_rejected(self):
        self.cli["paths"]["/tenants"]["post"]["x-cli-name"] = ["create"]
        with self.assertRaises(ValueError):
            validate(self.base, self.cli)


if __name__ == "__main__":
    unittest.main()

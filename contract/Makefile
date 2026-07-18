BASE_URL ?= http://localhost:4001

.PHONY: install contract
install:
	pip install -r requirements.txt

# Run the black-box contract suite against BASE_URL (the Go server by default).
# The same suite also runs green against the upstream Python/Django Plane API —
# point BASE_URL at that server to verify cross-language parity.
contract:
	BASE_URL=$(BASE_URL) pytest -v

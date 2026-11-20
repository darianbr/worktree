WTREE_PORT ?= 8000
export WTREE_PORT WTREE_CHECKOUT MAKEFILES_CHECKOUT

include .makefiles/modules/wtree/Makefile

.PHONY: start stop

# The server is deliberately project-owned and runs in the foreground.
start:
	$(MAKE) wtree up
	python3 -m http.server "$$WTREE_PORT" --bind 127.0.0.1

stop:
	$(MAKE) wtree down

.makefiles/modules/wtree/Makefile:
	@test -n "$$WTREE_CHECKOUT" || { echo 'WTREE_CHECKOUT is required' >&2; exit 1; }
	@bash "$$WTREE_CHECKOUT/scripts/bootstrap-make-module.sh"
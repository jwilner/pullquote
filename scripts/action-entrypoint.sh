#!/bin/sh

# GitHub actions passes input as environment variables. Here,
# we convert them to command-line arguments for pullquote.
# See ../action.yml for a description of each argument.

main() {
  if is_true "${INPUT_WALK}"; then
    args="-walk"
  fi
  if is_true "${INPUT_CHECK}"; then
    args="${args} -check"
  fi

  exec pullquote ${args} ${INPUT_FILES}
}

is_true() {
  case "${1}" in
  [Tt][Rr][Uu][Ee]) ;;
  [Oo][Nn]) ;;
  [Yy]) ;;
  [Yy][Ee][Ss]) ;;
  1) ;;
  *) return 1 ;;
  esac
}

main

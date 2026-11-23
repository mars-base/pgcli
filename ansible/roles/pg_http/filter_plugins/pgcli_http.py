"""Filters used by the pg_http role."""

from passlib.hash import bcrypt


def pgcli_bcrypt_verify(password, hashed):
    """Return True when password matches the bcrypt hash from the state file."""
    if not hashed:
        return False
    try:
        return bcrypt.verify(str(password), str(hashed))
    except (TypeError, ValueError):
        return False


class FilterModule:
    def filters(self):
        return {"pgcli_bcrypt_verify": pgcli_bcrypt_verify}

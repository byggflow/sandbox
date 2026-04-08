"""Error hierarchy for the Byggflow Sandbox SDK.

Mirrors the TypeScript SDK error types with Python idioms.
"""


class SandboxError(Exception):
    """Base error for all sandbox operations."""

    def __init__(self, message: str) -> None:
        super().__init__(message)
        self.message = message


class ConnectionError(SandboxError):
    """Raised when a connection to the daemon cannot be established or is lost."""

    def __init__(self, message: str) -> None:
        super().__init__(message)


class RpcError(SandboxError):
    """Raised when the daemon returns an RPC-level error."""

    def __init__(self, message: str, code: int) -> None:
        super().__init__(message)
        self.code = code


class TimeoutError(SandboxError):
    """Raised when an operation exceeds its deadline."""

    def __init__(self, message: str) -> None:
        super().__init__(message)


class FsError(SandboxError):
    """Raised for filesystem-specific failures inside the sandbox."""

    def __init__(self, message: str, fs_code: str) -> None:
        super().__init__(message)
        self.fs_code = fs_code


class CapacityError(SandboxError):
    """Raised when the daemon has no capacity to fulfil the request.

    ``retry_after`` indicates seconds before retrying.
    """

    def __init__(self, message: str, retry_after: float) -> None:
        super().__init__(message)
        self.retry_after = retry_after


class SessionReplacedError(ConnectionError):
    """Raised when another connection takes over this session."""

    def __init__(self) -> None:
        super().__init__("Session replaced by a new connection")

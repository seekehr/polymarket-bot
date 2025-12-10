from dataclasses import dataclass
from enum import Enum
from typing import Optional, Dict


@dataclass
class BuilderApiKeyCreds:
    key: str
    secret: str
    passphrase: str


class BuilderType(Enum):
    UNAVAILABLE = "UNAVAILABLE"
    LOCAL = "LOCAL"
    REMOTE = "REMOTE"


@dataclass
class RemoteBuilderConfig:
    """Remote builder configuration"""

    url: str
    token: Optional[str] = None


@dataclass
class RemoteSignerPayload:
    """Remote signer payload"""

    method: str
    path: str
    body: Optional[str] = None
    timestamp: Optional[int] = None


@dataclass
class BuilderHeaderPayload:
    """Builder header payload"""

    POLY_BUILDER_API_KEY: str
    POLY_BUILDER_TIMESTAMP: str
    POLY_BUILDER_PASSPHRASE: str
    POLY_BUILDER_SIGNATURE: str

    def to_dict(self) -> Dict[str, str]:
        """Convert to dictionary for use as headers"""
        return {
            "POLY_BUILDER_API_KEY": self.POLY_BUILDER_API_KEY,
            "POLY_BUILDER_TIMESTAMP": self.POLY_BUILDER_TIMESTAMP,
            "POLY_BUILDER_PASSPHRASE": self.POLY_BUILDER_PASSPHRASE,
            "POLY_BUILDER_SIGNATURE": self.POLY_BUILDER_SIGNATURE,
        }

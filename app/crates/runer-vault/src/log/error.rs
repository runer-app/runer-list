use std::fmt;

/// One code per violated rule, named after the Go sentinel it mirrors.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LogErrorCode {
    Malformed,
    InvalidSignature,
    HeadMismatch,
    NotOwner,
    NotMember,
    LeaveNotSelf,
    EpochMustIncrease,
    EpochMustHold,
    MissingEnvelope,
    MissingKeyChain,
    OwnerStays,
    TimestampSkew,
    AlreadyMember,
    InviteReused,
    InviteExpiry,
    InviteNotOpen,
    InviteExpired,
    JoinProof,
}

impl LogErrorCode {
    pub fn as_str(self) -> &'static str {
        use LogErrorCode as C;
        match self {
            C::Malformed => "Malformed",
            C::InvalidSignature => "InvalidSignature",
            C::HeadMismatch => "HeadMismatch",
            C::NotOwner => "NotOwner",
            C::NotMember => "NotMember",
            C::LeaveNotSelf => "LeaveNotSelf",
            C::EpochMustIncrease => "EpochMustIncrease",
            C::EpochMustHold => "EpochMustHold",
            C::MissingEnvelope => "MissingEnvelope",
            C::MissingKeyChain => "MissingKeyChain",
            C::OwnerStays => "OwnerStays",
            C::TimestampSkew => "TimestampSkew",
            C::AlreadyMember => "AlreadyMember",
            C::InviteReused => "InviteReused",
            C::InviteExpiry => "InviteExpiry",
            C::InviteNotOpen => "InviteNotOpen",
            C::InviteExpired => "InviteExpired",
            C::JoinProof => "JoinProof",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LogError {
    pub code: LogErrorCode,
    pub detail: Option<String>,
    /// Index of the offending entry when raised by `apply_entries`.
    pub index: Option<usize>,
}

impl LogError {
    pub fn new(code: LogErrorCode) -> Self {
        Self { code, detail: None, index: None }
    }

    pub fn with(code: LogErrorCode, detail: impl Into<String>) -> Self {
        Self { code, detail: Some(detail.into()), index: None }
    }

    pub fn malformed(detail: impl Into<String>) -> Self {
        Self::with(LogErrorCode::Malformed, detail)
    }
}

impl fmt::Display for LogError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if let Some(i) = self.index {
            write!(f, "entry {i}: ")?;
        }
        f.write_str(self.code.as_str())?;
        if let Some(d) = &self.detail {
            write!(f, ": {d}")?;
        }
        Ok(())
    }
}

impl std::error::Error for LogError {}

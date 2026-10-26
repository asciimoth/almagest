//! Trusted Windows named-pipe connection support for the desktop app.
//!
//! This small crate contains the Windows API boundary. The main application
//! forbids unsafe code and can use this checked interface.

#![cfg(windows)]

use std::{
    ffi::c_void,
    fmt::Write as _,
    fs::{File, OpenOptions},
    io,
    mem::{size_of, size_of_val},
    os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle},
    ptr, slice,
};

use windows_sys::Win32::{
    Foundation::{ERROR_INSUFFICIENT_BUFFER, GetLastError, LocalFree},
    Security::{
        Authorization::{GetSecurityInfo, SE_KERNEL_OBJECT},
        CreateWellKnownSid, EqualSid, GetLengthSid, GetTokenInformation,
        OWNER_SECURITY_INFORMATION, PSID, SECURITY_MAX_SID_SIZE, TOKEN_QUERY, TOKEN_USER,
        TokenUser, WinLocalSystemSid,
    },
    System::Threading::{GetCurrentProcess, OpenProcessToken},
};

const MACHINE_PIPE_NAME: &str = "almagest-admin";

/// Connect to a pipe and reject it unless its owner matches the trust class.
///
/// `trust` is `user` for the current process identity or `machine` for Local
/// System. An empty name selects the same derived name as `almagest-cli`.
///
/// # Errors
///
/// Returns an error if the trust class is invalid, Windows cannot resolve the
/// expected SID, the pipe cannot be opened, or the pipe owner is not trusted.
pub fn connect(trust: &str, name: &str) -> io::Result<File> {
    let expected = match trust {
        "user" => current_user_sid()?,
        "machine" => local_system_sid()?,
        _ => {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "unknown pipe trust class",
            ));
        }
    };
    let name = if name.is_empty() {
        match trust {
            "user" => format!("almagest-admin-user-{}", sid_string(expected.sid())?),
            "machine" => MACHINE_PIPE_NAME.to_owned(),
            _ => unreachable!("trust was validated above"),
        }
    } else {
        name.to_owned()
    };
    let path = format!(r"\\.\pipe\{name}");
    let pipe = OpenOptions::new().read(true).write(true).open(&path)?;
    verify_owner(&pipe, expected.sid()).map_err(|error| {
        io::Error::new(error.kind(), format!("verify owner of {path}: {error}"))
    })?;
    Ok(pipe)
}

struct SidBuffer {
    words: Vec<usize>,
    sid_offset: usize,
}

impl SidBuffer {
    fn sid(&self) -> PSID {
        // The offset points into the stable heap allocation owned by `words`.
        unsafe {
            self.words
                .as_ptr()
                .cast::<u8>()
                .add(self.sid_offset)
                .cast_mut()
                .cast()
        }
    }
}

fn current_user_sid() -> io::Result<SidBuffer> {
    let mut token = ptr::null_mut();
    // SAFETY: `token` is a valid output pointer.
    if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &raw mut token) } == 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: OpenProcessToken returned a newly owned, non-null handle.
    let token = unsafe { OwnedHandle::from_raw_handle(token) };

    let mut bytes = 0;
    // SAFETY: This documented first call obtains the required buffer size.
    let first = unsafe {
        GetTokenInformation(
            token.as_raw_handle(),
            TokenUser,
            ptr::null_mut(),
            0,
            &raw mut bytes,
        )
    };
    // SAFETY: `GetLastError` is read immediately after the failed API call.
    let first_error = unsafe { GetLastError() };
    if first != 0 || first_error != ERROR_INSUFFICIENT_BUFFER {
        return Err(io::Error::from_raw_os_error(first_error.cast_signed()));
    }

    let words = usize::try_from(bytes)
        .ok()
        .and_then(|value| value.checked_add(size_of::<usize>() - 1))
        .map(|value| value / size_of::<usize>())
        .ok_or_else(|| io::Error::other("token information is too large"))?;
    let mut storage = vec![0_usize; words];
    // SAFETY: The aligned allocation has the size returned by Windows.
    let ok = unsafe {
        GetTokenInformation(
            token.as_raw_handle(),
            TokenUser,
            storage.as_mut_ptr().cast(),
            bytes,
            &raw mut bytes,
        )
    };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }

    // SAFETY: A successful TokenUser query starts with a valid TOKEN_USER.
    let sid = unsafe { storage.as_ptr().cast::<TOKEN_USER>().read().User.Sid };
    let base = storage.as_ptr().cast::<u8>() as usize;
    let sid_address = sid as usize;
    let sid_offset = sid_address
        .checked_sub(base)
        .filter(|offset| *offset < size_of_val(storage.as_slice()))
        .ok_or_else(|| io::Error::other("Windows returned a SID outside the token buffer"))?;
    Ok(SidBuffer {
        words: storage,
        sid_offset,
    })
}

fn local_system_sid() -> io::Result<SidBuffer> {
    let words = usize::try_from(SECURITY_MAX_SID_SIZE)
        .expect("SECURITY_MAX_SID_SIZE fits usize")
        .div_ceil(size_of::<usize>());
    let mut storage = vec![0_usize; words];
    let mut bytes = SECURITY_MAX_SID_SIZE;
    // SAFETY: `storage` is large enough for every well-known SID.
    if unsafe {
        CreateWellKnownSid(
            WinLocalSystemSid,
            ptr::null_mut(),
            storage.as_mut_ptr().cast(),
            &raw mut bytes,
        )
    } == 0
    {
        return Err(io::Error::last_os_error());
    }
    Ok(SidBuffer {
        words: storage,
        sid_offset: 0,
    })
}

fn verify_owner(pipe: &File, expected: PSID) -> io::Result<()> {
    let handle = pipe.as_raw_handle().cast::<c_void>();
    let mut owner = ptr::null_mut();
    let mut descriptor = ptr::null_mut();
    // SAFETY: The pipe handle is live, requested output pointers are valid,
    // and Windows allocates `descriptor` for release with LocalFree.
    let status = unsafe {
        GetSecurityInfo(
            handle,
            SE_KERNEL_OBJECT,
            OWNER_SECURITY_INFORMATION,
            &raw mut owner,
            ptr::null_mut(),
            ptr::null_mut(),
            ptr::null_mut(),
            &raw mut descriptor,
        )
    };
    if status != 0 {
        return Err(io::Error::from_raw_os_error(
            i32::try_from(status).unwrap_or(i32::MAX),
        ));
    }
    // SAFETY: Both SIDs are valid for this call; `descriptor` owns `owner`.
    let equal = unsafe { EqualSid(owner, expected) } != 0;
    // SAFETY: `descriptor` was allocated by GetSecurityInfo.
    unsafe { LocalFree(descriptor.cast()) };
    if !equal {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "named-pipe owner does not match the requested trust class",
        ));
    }
    Ok(())
}

fn sid_string(sid: PSID) -> io::Result<String> {
    // SAFETY: Callers provide a valid SID for the duration of this function.
    let length = unsafe { GetLengthSid(sid) };
    if length < 8 {
        return Err(io::Error::other("Windows returned an invalid SID"));
    }
    // SAFETY: GetLengthSid returned the readable byte length of `sid`.
    let bytes = unsafe { slice::from_raw_parts(sid.cast::<u8>(), length as usize) };
    let count = usize::from(bytes[1]);
    if bytes.len() != 8 + count * 4 {
        return Err(io::Error::other("Windows returned an invalid SID length"));
    }
    let authority = bytes[2..8]
        .iter()
        .fold(0_u64, |value, byte| (value << 8) | u64::from(*byte));
    let mut result = format!("S-{}-{authority}", bytes[0]);
    for chunk in bytes[8..].chunks_exact(4) {
        let value = u32::from_le_bytes(chunk.try_into().expect("four-byte SID component"));
        write!(&mut result, "-{value}").expect("writing to a String cannot fail");
    }
    Ok(result)
}

// Shared response parsing and safe API error presentation.
"use strict";

async function readJSON(response) {
  try {
    return await response.json();
  } catch {
    return null;
  }
}

function messageForAPIError(response, payload, operation) {
  const code = payload?.error?.code;
  if (code === "unauthorized") {
    return "Email or password is incorrect.";
  }
  if (code === "rate_limited") {
    return "Too many attempts. Wait a little and try again.";
  }
  if (code === "invalid_request") {
    return "Check the information and try again.";
  }
  if (operation === "logout") {
    return "We couldn't confirm sign-out. Try again or refresh to check your session.";
  }
  if (response.status >= 500) {
    return "We couldn't complete that request right now. Please try again.";
  }
  return operation === "register"
    ? "We couldn't create your account. Check the information and try again."
    : "We couldn't sign you in. Check your details and try again.";
}

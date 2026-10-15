// The app's own commands, declared so that each needs an explicit
// permission: the `default` capability lists them, and nothing else is
// reachable from the webview (client-spec §6.1).
const COMMANDS: &[&str] = &[
    "vault_accounts",
    "vault_set_inbox",
    "vault_sign_in",
    "vault_start_sign_up",
    "vault_solve_sign_up_pow",
    "vault_confirm_sign_up",
    "vault_sign_out",
    "vault_show_words",
    "vault_seal_account_settings",
    "vault_open_account_settings",
    "vault_sign_server_message",
    "vault_load_identity",
    "vault_change_account_key",
    "vault_commit_account_key",
    "vault_apply_log",
    "vault_list_state",
    "vault_check_head",
    "vault_forget_list",
    "vault_create_list",
    "vault_rotate",
    "vault_seal_content",
    "vault_open_contents",
    "vault_add_member",
    "vault_remove_member",
    "vault_leave",
    "vault_known_contacts",
    "vault_safety_code",
    "vault_create_invite",
    "vault_uninvite",
    "vault_open_invite_link",
    "vault_read_invite_package",
    "vault_join",
    "store_get",
    "store_range",
    "store_write",
    "net_request",
    "net_socket_open",
    "net_socket_send",
    "net_socket_close",
    "notify_replace_all",
    "notify_permission",
    "notify_request_permission",
    "links_initial",
];

fn main() {
    tauri_build::try_build(
        tauri_build::Attributes::new().app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("tauri build");
}

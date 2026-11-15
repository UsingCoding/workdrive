workdrive() {
    local _wd_tmp='' _wd_file='' _wd_status=0 _wd_valid=0
    local _wd_version='' _wd_destination='' _wd_action='' _wd_token='' _wd_token_body='' _wd_extra=''
# @SETUP@

    # Completion has history but no navigation capability or temporary files.
    case ${1-} in
        __complete|__completeNoDesc)
# @RESTORE@
            (
                builtin unset WORKDRIVE_DIRECTIVE_CD_FILE
                WORKDRIVE_PREVIOUS_WORKSPACE=${_workdrive_previous_workspace-} command workdrive "$@"
            )
            return $?
            ;;
    esac

    if _wd_tmp=$(command mktemp -d "${TMPDIR:-/tmp}/workdrive.XXXXXXXXXX"); then
        case $_wd_tmp in
            /*)
                _wd_file=$_wd_tmp/directive
                if (umask 077; : > "$_wd_file"); then
                    if WORKDRIVE_DIRECTIVE_CD_FILE=$_wd_file WORKDRIVE_PREVIOUS_WORKSPACE=${_workdrive_previous_workspace-} command workdrive "$@"; then
                        _wd_status=0
                    else
                        _wd_status=$?
                    fi
                else
                    builtin printf '%s\n' 'workdrive: cannot create private shell handoff file' >&2
                    _wd_status=1
                fi
                ;;
            *)
                builtin printf '%s\n' 'workdrive: mktemp did not create an absolute private directory' >&2
                _wd_status=1
                ;;
        esac
    else
        _wd_status=$?
        builtin printf '%s\n' 'workdrive: cannot create private shell handoff directory' >&2
    fi

    if [[ $_wd_status == 0 ]]; then
        if [[ ! -f $_wd_file || -L $_wd_file ]]; then
            _wd_status=1
            builtin printf '%s\n' 'workdrive: shell handoff file is missing or unsafe' >&2
        elif [[ -s $_wd_file ]]; then
            # Read literal NUL fields, including embedded and trailing newlines.
            # The final read must find neither another field nor partial bytes.
            if {
                IFS= builtin read -r -d '' _wd_version &&
                IFS= builtin read -r -d '' _wd_destination &&
                IFS= builtin read -r -d '' _wd_action &&
                IFS= builtin read -r -d '' _wd_token &&
                ! { IFS= builtin read -r -d '' _wd_extra || [[ -n $_wd_extra ]]; }
            } < "$_wd_file"; then
                case $_wd_version:$_wd_action in
                    1:keep) [[ -z $_wd_token ]] && _wd_valid=1 ;;
                    1:replace)
                        case $_wd_token in
                            v1.?*)
                                _wd_token_body=${_wd_token#v1.}
                                case $_wd_token_body in
                                    *[!a-zA-Z0-9_-]*) ;;
                                    *) _wd_valid=1 ;;
                                esac
                                ;;
                        esac
                        ;;
                esac
            fi
            case $_wd_destination in
                /*) ;;
                *) _wd_valid=0 ;;
            esac
            if [[ $_wd_valid == 1 ]]; then
                if builtin cd -- "$_wd_destination"; then
                    if [[ $_wd_action == replace ]]; then
                        _workdrive_previous_workspace=$_wd_token
                    fi
                else
                    _wd_status=$?
                fi
            else
                builtin printf '%s\n' 'workdrive: invalid shell navigation handoff' >&2
                _wd_status=1
            fi
        fi
    fi

    if [[ -n $_wd_tmp ]]; then
        command rm -rf -- "$_wd_tmp" || :
    fi
# @RESTORE@
    return "$_wd_status"
}

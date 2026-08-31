export interface ChannelStatus {
  name: string;
  enabled: boolean;
  running: boolean;
  state?: "disabled" | "starting" | "running" | "restarting" | "stopped" | "error";
  detail?: string;
  updated_at: string;
  last_start_at?: string;
  last_stop_at?: string;
  last_poll_at?: string;
  last_inbound_at?: string;
  last_ack_at?: string;
  last_reply_at?: string;
  last_duplicate_at?: string;
  last_error?: string;
  last_event?: string;
  last_delivery?: DeliveryRecord;
  last_access?: AccessDecision;
  capabilities?: ChannelCapabilities;
}

export interface ChannelStatusReport {
  generated_at: string;
  channels: ChannelStatus[];
  deliveries?: DeliveryRecord[];
}

export interface ChannelCapabilities {
  delivery?: boolean;
  auth_login?: boolean;
  media?: boolean;
  streaming?: boolean;
  typing?: boolean;
  status?: boolean;
  allow_from?: boolean;
  session_modes?: string[];
}

export interface DeliveryRecord {
  id: string;
  target_kind?: string;
  channel?: string;
  session_id?: string;
  status: string;
  attempts: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
  delivered_at?: string;
  failed_at?: string;
}

export interface AccessDecision {
  action: "allow" | "deny" | "approval_required" | string;
  reason?: string;
  channel?: string;
  sender_id?: string;
  platform_id?: string;
  thread_id?: string;
  decided_at: string;
}

export interface WeixinAuthAccount {
  base_url?: string;
  cdn_base_url?: string;
  ilink_bot_id?: string;
  ilink_user_id?: string;
  updated_at?: string;
}

export interface WeixinAuthLogin {
  active: boolean;
  state: string;
  raw_status?: string;
  message?: string;
  qr_code?: string;
  qr_code_img_url?: string;
  qr_code_img_value?: string;
  started_at?: string;
  last_checked_at?: string;
  updated_at?: string;
}

export interface WeixinAuthStatus {
  account_id: string;
  enabled: boolean;
  configured: boolean;
  state_dir: string;
  account?: WeixinAuthAccount;
  login?: WeixinAuthLogin;
}

export interface DeliveryTarget {
  kind?: string;
  session_id?: string;
  channel?: string;
  session_key?: string;
  recipient?: string;
  metadata?: Record<string, string>;
}

export interface CronSchedule {
  type: string;
  at?: string;
  every_seconds?: number;
  cron_expr?: string;
}

export interface CronJob {
  id: string;
  name?: string;
  message: string;
  timezone?: string;
  schedule: CronSchedule;
  session_mode?: string;
  delivery_target?: DeliveryTarget;
  enabled: boolean;
  created_by?: string;
  created_from_session?: string;
  created_at?: string;
  updated_at?: string;
  last_run_at?: string;
  next_run_at?: string;
  last_status?: string;
  last_error?: string;
}

export interface CronRunLog {
  id: string;
  job_id: string;
  session_id?: string;
  turn_id?: string;
  status: string;
  error?: string;
  delivery_target?: DeliveryTarget;
  started_at?: string;
  finished_at?: string;
}

export interface HeartbeatRule {
  id: string;
  enabled: boolean;
  interval_seconds: number;
  timezone?: string;
  active_hours_start?: string;
  active_hours_end?: string;
  session_mode?: string;
  delivery_target?: DeliveryTarget;
  prompt_override?: string;
  watchdog_script?: string;
  created_by?: string;
  created_from_session?: string;
  created_at?: string;
  updated_at?: string;
  last_run_at?: string;
  next_run_at?: string;
  last_status?: string;
  last_error?: string;
}

export interface HeartbeatRunLog {
  id: string;
  rule_id: string;
  session_id?: string;
  turn_id?: string;
  status: string;
  error?: string;
  suppressed?: boolean;
  watchdog_output?: string;
  delivery_target?: DeliveryTarget;
  started_at?: string;
  finished_at?: string;
}


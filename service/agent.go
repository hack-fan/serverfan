package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hack-fan/x/xerr"
	"github.com/rs/xid"
	"github.com/vmihailenco/msgpack/v5"
	"gorm.io/gorm"

	"github.com/hack-fan/skadi/types"
)

func agentOnlineKey(aid string) string {
	return "agent:online:" + aid
}

// AgentAdd create a server side agent
func (s *Service) AgentAdd(uid string, info *types.AgentBasic) (*types.Agent, error) {
	// check
	if info == nil || info.Name == "" {
		return nil, xerr.New(400, "MissingName", "agent name is required")
	}
	if types.RESERVED.Contains(info.Name) || types.RESERVED.Contains(info.Alias) {
		return nil, xerr.New(400, "InvalidName", "the name is reserved by system")
	}
	if info.Alias != "" && types.RESERVED.Contains(info.Alias) {
		return nil, xerr.New(400, "InvalidAlias", "the alias is reserved by system")
	}
	// create
	var agent = &types.Agent{
		ID:     xid.New().String(),
		UserID: uid,
		Name:   info.Name,
		Alias:  info.Alias,
		Remark: info.Remark,
		Secret: xid.New().String(),
	}
	err := s.db.Create(agent).Error
	if err != nil {
		return nil, fmt.Errorf("create new agent to db failed: %w", err)
	}
	return agent, nil
}

// UserAgents return user's all agent
func (s *Service) UserAgents(uid string) ([]*types.Agent, error) {
	var agents = make([]*types.Agent, 0)
	err := s.db.Find(&agents, "user_id = ?", uid).Error
	if err != nil {
		return nil, fmt.Errorf("select agent from db error: %w", err)
	}
	return agents, nil
}

func (s *Service) FindUserAgentByName(uid, name string) (id string, ok bool, err error) {
	var agent = new(types.Agent)
	err = s.db.Select("id").First(agent, "user_id = ? and name = ?", uid, name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err := s.db.Select("id").First(agent, "user_id = ? and alias = ?", uid, name).Error
		// name and alias both not found
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		} else if err != nil {
			return "", false, fmt.Errorf("find user agent db error: %w", err)
		}
		return agent.ID, true, nil
	} else if err != nil {
		return "", false, fmt.Errorf("find user agent db error:%w", err)
	}
	return agent.ID, true, nil
}

// call after every agent job pull
func (s *Service) AgentOnline(aid, ip string) {
	// save ip after first request
	if !s.IsAgentOnline(aid) {
		err := s.db.Model(&types.Agent{}).Where("id = ?", aid).Update("ip", ip).Error
		if err != nil {
			go s.notify(fmt.Errorf("save agent %s ip to db failed: %w", aid, err))
		}
	}
	// refresh online status
	err := s.kv.Set(s.ctx, agentOnlineKey(aid), time.Now().Unix(), 3*time.Minute).Err()
	if err != nil {
		go s.notify(fmt.Errorf("set agent %s online failed: %w", aid, err))
	}
}

// call after the watcher found agent status switch to offline
func (s *Service) AgentOffline(aid string) {
	// clear the agent queue, set all job as expired
	for {
		var job = new(types.JobBasic)
		data, err := s.kv.RPop(s.ctx, agentQueueKey(aid)).Bytes()
		s.log.Debugw("pop", "data", string(data), "err", err)
		if err == redis.Nil {
			break
		} else if err != nil {
			go s.notify(fmt.Errorf("pop job from queue error: %w", err))
			return
		}
		err = msgpack.Unmarshal(data, job)
		if err != nil {
			s.notify(fmt.Errorf("msgpack unmarshal job basic error: %w", err))
			return
		}
		s.JobExpire(job.ID)
	}
	// change agent activity log
	err := s.db.Model(&types.Agent{}).Where("id = ?", aid).Update("activated_at", time.Now()).Error
	if err != nil {
		go s.notify(fmt.Errorf("save agent %s activity to db failed: %w", aid, err))
	}
}

func (s *Service) IsAgentOnline(aid string) bool {
	cnt, err := s.kv.Exists(s.ctx, agentOnlineKey(aid)).Result()
	if err != nil {
		go s.notify(fmt.Errorf("check agent %s online failed: %w", aid, err))
		return false
	}
	if cnt > 0 {
		return true
	}
	return false
}

func (s *Service) AgentSecret(aid string) (string, error) {
	var agent = new(types.Agent)
	err := s.db.Select("secret").First(agent, "id = ?", aid).Error
	if err != nil {
		return "", fmt.Errorf("get agent secret from db failed: %w", err)
	}
	return agent.Secret, nil
}

func (s *Service) AgentSecretReset(aid string) (string, error) {
	// find old
	old, err := s.AgentSecret(aid)
	if err != nil {
		return "", err
	}
	// new secret
	secret := xid.New().String()
	err = s.db.Model(&types.Agent{}).Update("secret", secret).Where("id = ?", aid).Error
	if err != nil {
		return "", fmt.Errorf("update agent secret failed: %w", err)
	}
	// clear old
	s.clearAgentAuthCache(old)

	return secret, nil
}

func (s *Service) AgentDelete(aid string) error {
	// check online
	if s.IsAgentOnline(aid) {
		return xerr.Newf(400, "AgentOnline", "you can not remove online agent")
	}
	// delete jobs and agent
	err := s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Delete(&types.Job{}, "agent_id = ?", aid).Error
		if err != nil {
			return fmt.Errorf("remove agent jobs failed: %w", err)
		}
		err = tx.Delete(&types.Agent{}, "id = ?", aid).Error
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("remove agent failed: %w", err)
	}
	return nil
}
